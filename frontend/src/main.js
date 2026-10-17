import './style.css';
import { GetNodes, StartPrefer, CancelPrefer, PausePrefer, ResumePrefer, SaveCSV, SaveImage, StartSpeedTest, CancelSpeedTest, PauseSpeedTest, ResumeSpeedTest, GetAutostart, SetAutostart, HideToTray } from '../wailsjs/go/main/App';
import { EventsOn, WindowMinimise, WindowToggleMaximise, WindowGetPosition, WindowSetPosition, BrowserOpenURL } from '../wailsjs/runtime/runtime';
import { StartQZhuaLogin, LogoutQZhua, CancelQZhuaLogin } from '../wailsjs/go/main/App';

const $ = (id) => document.getElementById(id);

// ---------- 设置持久化 ----------
const settings = {
    get addr() { return localStorage.getItem('tunnelAddr') || ''; },
    set addr(v) { localStorage.setItem('tunnelAddr', v); },
    get secs() { return parseInt(localStorage.getItem('testSecs') || '10', 10); },
    set secs(v) { localStorage.setItem('testSecs', String(v)); },
};
// 已选卡片（多选，点击卡片切换，全选按钮批量，持久化）
const selected = new Set(JSON.parse(localStorage.getItem('pickedNodeIds') || '[]'));
const saveSel = () => localStorage.setItem('pickedNodeIds', JSON.stringify([...selected]));
const bandwidthResults = new Map();
let speedNodeId = null;

// ---------- 导航 ----------
document.querySelectorAll('.nav-item').forEach((btn) => {
    btn.addEventListener('click', () => {
        document.querySelectorAll('.nav-item').forEach((b) => b.classList.remove('active'));
        btn.classList.add('active');
        document.querySelectorAll('.view').forEach((v) => v.classList.remove('active'));
        $('view-' + btn.dataset.view).classList.add('active');
    });
});

// ---------- 自定义标题栏 ----------
$('tbMin').addEventListener('click', () => WindowMinimise());
$('tbMax').addEventListener('click', () => WindowToggleMaximise());
$('tbClose').addEventListener('click', () => HideToTray());

let qzhuaLoggedIn = false;
let loginVerifyUrl = '';

const showLoginStep1 = () => {
    $('loginStep1').classList.remove('hidden');
    $('loginStep2').classList.add('hidden');
};
const openLoginModal = () => {
    showLoginStep1();
    $('loginError').classList.add('hidden');
    $('loginModal').classList.remove('hidden');
};
const closeLoginModal = (cancelLogin) => {
    $('loginModal').classList.add('hidden');
    if (cancelLogin) CancelQZhuaLogin().catch(() => {});
};

EventsOn('qzhuaAuth', (event) => {
    if (event.status === 'logged_in') {
        qzhuaLoggedIn = true;
        $('accTitle').textContent = event.username || '轻爪用户';
        $('accSubtitle').textContent = '点击退出';
        $('sideAccount').classList.add('logged-in');
        $('loginModal').classList.add('hidden');
        return;
    }
    qzhuaLoggedIn = false;
    $('sideAccount').classList.remove('logged-in');
    $('accTitle').textContent = '未登录';
    if (event.status === 'waiting') {
        if (event.verifyUrl) loginVerifyUrl = event.verifyUrl;
        $('accSubtitle').textContent = '等待授权…';
        return;
    }
    if (event.status === 'error') {
        $('accSubtitle').textContent = event.error || '点击登录';
        if (!$('loginModal').classList.contains('hidden')) {
            showLoginStep1();
            $('loginError').textContent = event.error || '登录失败，请重试';
            $('loginError').classList.remove('hidden');
        }
        return;
    }
    $('accSubtitle').textContent = '点击登录';
});

$('sideAccount').addEventListener('click', () => {
    if (qzhuaLoggedIn) {
        LogoutQZhua();
        return;
    }
    openLoginModal();
});

$('loginAuthBtn').addEventListener('click', async () => {
    const btn = $('loginAuthBtn');
    btn.disabled = true;
    $('loginError').classList.add('hidden');
    try {
        const res = await StartQZhuaLogin();
        loginVerifyUrl = res.verifyUrl || '';
        $('loginStep1').classList.add('hidden');
        $('loginStep2').classList.remove('hidden');
    } catch (err) {
        $('loginError').textContent = String(err);
        $('loginError').classList.remove('hidden');
    } finally {
        btn.disabled = false;
    }
});

$('loginOpenBrowser').addEventListener('click', () => {
    if (loginVerifyUrl) BrowserOpenURL(loginVerifyUrl);
});

$('loginCancelBtn').addEventListener('click', async () => {
    await CancelQZhuaLogin().catch(() => {});
    openLoginModal();
});

$('loginClose').addEventListener('click', () => closeLoginModal(true));
$('loginBackdrop').addEventListener('click', () => closeLoginModal(true));

$('loginRegister').addEventListener('click', (e) => {
    e.preventDefault();
    BrowserOpenURL('https://account-api.qzhua.net/register');
});

// ---------- 带宽测速 ----------
let speedRunning = false;
let speedPaused = false;
const speedSamples = [];
let lastSpeedSampleSecond = -1;
let bandwidthRunning = false;
const formatSpeed = (value) => Number(value || 0).toFixed(1);
const smoothPath = (points) => {
    if (points.length < 2) return '';
    let d = `M ${points[0][0].toFixed(2)} ${points[0][1].toFixed(2)}`;
    for (let i = 0; i < points.length - 1; i++) {
        const p0 = points[i - 1] || points[i];
        const p1 = points[i];
        const p2 = points[i + 1];
        const p3 = points[i + 2] || p2;
        const c1x = p1[0] + (p2[0] - p0[0]) / 6;
        const c1y = p1[1] + (p2[1] - p0[1]) / 6;
        const c2x = p2[0] - (p3[0] - p1[0]) / 6;
        const c2y = p2[1] - (p3[1] - p1[1]) / 6;
        d += ` C ${c1x.toFixed(2)} ${c1y.toFixed(2)}, ${c2x.toFixed(2)} ${c2y.toFixed(2)}, ${p2[0].toFixed(2)} ${p2[1].toFixed(2)}`;
    }
    return d;
};
let chartSeq = 0;
const chartHoverBound = new WeakSet();
const ensureChartHover = (chart) => {
    if (chartHoverBound.has(chart)) return;
    chartHoverBound.add(chart);
    const overlay = document.createElement('div');
    overlay.className = 'chart-hover hidden';
    overlay.innerHTML = '<div class="hover-vline"></div><div class="hover-dot hover-dot-down"></div><div class="hover-dot hover-dot-up"></div>';
    const tip = document.createElement('div');
    tip.className = 'chart-tooltip hidden';
    chart.appendChild(overlay);
    chart.appendChild(tip);
    const hide = () => {
        overlay.classList.add('hidden');
        tip.classList.add('hidden');
    };
    chart.addEventListener('mouseleave', hide);
    chart.addEventListener('mousemove', (e) => {
        const svg = chart.querySelector('svg.speed-curve');
        const data = svg?.chartData;
        if (!data || !data.list.length) {
            hide();
            return;
        }
        const rect = chart.getBoundingClientRect();
        const px = Math.max(0, Math.min(100, ((e.clientX - rect.left) / Math.max(rect.width, 1)) * 100));
        const idx = Math.max(0, Math.min(data.list.length - 1, Math.round((px / 100) * (data.list.length - 1))));
        const x = data.x[idx];
        const item = data.list[idx];
        overlay.classList.remove('hidden');
        const vline = overlay.querySelector('.hover-vline');
        const dotDown = overlay.querySelector('.hover-dot-down');
        const dotUp = overlay.querySelector('.hover-dot-up');
        vline.style.left = `${x}%`;
        dotDown.style.left = `${x}%`;
        dotDown.style.top = `${data.downY[idx]}%`;
        dotUp.style.left = `${x}%`;
        dotUp.style.top = `${data.upY[idx]}%`;
        tip.innerHTML = `<div class="ct-title">第 ${item.t} 秒</div><div class="ct-row"><i class="ct-dot down"></i>下载 ${formatSpeed(item.down)} Mbps</div><div class="ct-row"><i class="ct-dot up"></i>上传 ${formatSpeed(item.up)} Mbps</div>`;
        tip.classList.remove('hidden');
        const tipW = 140;
        let left = e.clientX - rect.left + 14;
        if (left + tipW > rect.width - 6) left = e.clientX - rect.left - tipW - 10;
        tip.style.left = `${Math.max(6, left)}px`;
        tip.style.top = `${Math.max(6, e.clientY - rect.top - 64)}px`;
    });
};
const drawSpeedChart = () => {
    document.querySelectorAll('.chart-area').forEach((chart) => {
        ensureChartHover(chart);
        const previous = chart.querySelector('svg.speed-curve');
        if (previous) previous.remove();
        const empty = chart.querySelector('.chart-empty');
        if (!speedSamples.length) {
            if (empty) empty.classList.remove('hidden');
            chart.querySelector('.chart-hover')?.classList.add('hidden');
            chart.querySelector('.chart-tooltip')?.classList.add('hidden');
            return;
        }
        if (empty) empty.classList.add('hidden');
        const list = speedSamples.slice(-64);
        const max = Math.max(...list.map((item) => Math.max(item.down, item.up)), 1);
        const smoothed = (key) => {
            const values = list.map((item) => item[key]);
            return values.map((value, index) => {
                const prev = values[Math.max(index - 1, 0)];
                const next = values[Math.min(index + 1, values.length - 1)];
                return (prev + value * 2 + next) / 4;
            });
        };
        const toPoints = (values) => {
            const data = values.length === 1 ? [values[0], values[0]] : values;
            return data.map((value, index) => [
                (index / Math.max(data.length - 1, 1)) * 100,
                96 - (value / max) * 88,
            ]);
        };
        const downPts = toPoints(smoothed('down'));
        const upPts = toPoints(smoothed('up'));
        const downPath = smoothPath(downPts);
        const upPath = smoothPath(upPts);
        const gid = `speed${++chartSeq}`;
        const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
        svg.setAttribute('class', 'speed-curve');
        svg.setAttribute('viewBox', '0 0 100 100');
        svg.setAttribute('preserveAspectRatio', 'none');
        svg.innerHTML = `<defs><linearGradient id="${gid}Down" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#e8eaed" stop-opacity=".20"/><stop offset="1" stop-color="#e8eaed" stop-opacity="0"/></linearGradient><linearGradient id="${gid}Up" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#8b939e" stop-opacity=".16"/><stop offset="1" stop-color="#8b939e" stop-opacity="0"/></linearGradient></defs><path d="${downPath} L 100 100 L 0 100 Z" fill="url(#${gid}Down)"/><path d="${upPath} L 100 100 L 0 100 Z" fill="url(#${gid}Up)"/><path d="${upPath}" fill="none" stroke="#8b939e" stroke-width="1.4" vector-effect="non-scaling-stroke" stroke-linecap="round" stroke-linejoin="round"/><path d="${downPath}" fill="none" stroke="#e8eaed" stroke-width="1.6" vector-effect="non-scaling-stroke" stroke-linecap="round" stroke-linejoin="round"/>`;
        svg.chartData = {
            list,
            x: downPts.map((p) => p[0]),
            downY: downPts.map((p) => p[1]),
            upY: upPts.map((p) => p[1]),
        };
        chart.appendChild(svg);
    });
};
const setSpeedRunning = (running) => {
    speedRunning = running;
    bandwidthRunning = running;
    if (!running) speedPaused = false;
    $('speedBtn').classList.remove('hidden');
    $('speedCancelBtn').classList.toggle('hidden', !running);
    $('speedBtn').textContent = speedPaused ? '继续' : (running ? '暂停' : '开始测速');
    $('speedBtn').classList.toggle('speed-resume', speedPaused);
    $('speedState').textContent = running ? '测速中' : '待测速';
};
EventsOn('speed', (ev) => {
    const sampleSecond = Math.floor(ev.elapsed);
    const shouldDrawSample = sampleSecond > lastSpeedSampleSecond;
    if (shouldDrawSample) {
        speedSamples.push({ up: ev.up, down: ev.down, t: sampleSecond });
        if (speedSamples.length > 60) speedSamples.shift();
        lastSpeedSampleSecond = sampleSecond;
    }
    if ($('speedDown')) $('speedDown').innerHTML = `${formatSpeed(ev.down)}<em> Mbps</em>`;
    if ($('speedUp')) $('speedUp').innerHTML = `${formatSpeed(ev.up)}<em> Mbps</em>`;
    if ($('detailDown')) $('detailDown').textContent = `${formatSpeed(ev.down)} Mbps`;
    if ($('detailUp')) $('detailUp').textContent = `${formatSpeed(ev.up)} Mbps`;
    if ($('speedProgress')) $('speedProgress').innerHTML = `${Math.round(ev.progress * 100)}<em>%</em>`;
    $('bandwidthProgressFill').style.width = `${Math.round(ev.progress * 100)}%`;
    if ($('speedElapsed')) $('speedElapsed').textContent = `${ev.elapsed.toFixed(1)} / ${ev.total.toFixed(0)} 秒`;
    if (shouldDrawSample) drawSpeedChart();
});
EventsOn('speedDone', (ev) => {
    setSpeedRunning(false);
    if ($('speedDown')) $('speedDown').innerHTML = `${formatSpeed(ev.downAvg)}<em> Mbps</em>`;
    if ($('speedUp')) $('speedUp').innerHTML = `${formatSpeed(ev.upAvg)}<em> Mbps</em>`;
    if ($('detailDown')) $('detailDown').textContent = `${formatSpeed(ev.downAvg)} Mbps`;
    if ($('detailUp')) $('detailUp').textContent = `${formatSpeed(ev.upAvg)} Mbps`;
    if ($('detailAverage')) $('detailAverage').textContent = `${formatSpeed((ev.downAvg + ev.upAvg) / 2)} Mbps`;
    if ($('detailDuration')) $('detailDuration').textContent = settings.secs + ' 秒';
    if ($('speedProgress')) $('speedProgress').innerHTML = `${ev.cancelled ? '已停止' : '完成'}`;
    $('bandwidthProgressFill').style.width = ev.cancelled ? '0%' : '100%';
    $('speedElapsed').textContent = ev.cancelled ? '测速已停止' : '测速完成';
    $('currentSpeedNode').textContent = ev.cancelled ? '已停止' : '已完成';
    if (!ev.cancelled && speedNodeId) {
        bandwidthResults.set(speedNodeId, {
            ...(bandwidthResults.get(speedNodeId) || {}),
            down: formatSpeed(ev.downAvg),
            up: formatSpeed(ev.upAvg),
            average: formatSpeed((ev.downAvg + ev.upAvg) / 2),
            duration: String(settings.secs),
        });
        if (speedNodeId !== '__last__') bandwidthResults.delete('__last__');
        buildBandwidthGroups();
    }
    if (ev.cancelled) $('bandwidthLeaderboard')?.classList.add('hidden');
    else renderLeaderboard('bandwidthLeaderboard', 'bandwidthRankingList', 'bandwidthLeaderboardMeta', bandwidthRanking());
    if (ev.cancelled) resetRings();
    const hasData = [...bandwidthResults.entries()].some(([k, r]) => k !== '__last__' && r && (r.delay !== undefined || r.down));
    $('bandwidthExportBtn').disabled = !hasData;
});
const startSpeedRun = async () => {
    speedSamples.length = 0;
    lastSpeedSampleSecond = -1;
    drawSpeedChart();
    resetRings();
    $('bandwidthLeaderboard')?.classList.add('hidden');
    if ($('speedDown')) $('speedDown').innerHTML = '--<em> Mbps</em>';
    if ($('speedUp')) $('speedUp').innerHTML = '--<em> Mbps</em>';
    if ($('detailDown')) $('detailDown').textContent = '--';
    if ($('detailUp')) $('detailUp').textContent = '--';
    if ($('detailAverage')) $('detailAverage').textContent = '--';
    if ($('detailDuration')) $('detailDuration').textContent = '--';
    if ($('speedProgress')) $('speedProgress').innerHTML = '0<em>%</em>';
    $('bandwidthProgressFill').style.width = '0%';
    $('speedElapsed').textContent = '正在连接';
    const currentNode = nodes.find((node) => selected.has(String(node.id)));
    speedNodeId = currentNode ? String(currentNode.id) : '__last__';
    $('currentSpeedNode').textContent = currentNode?.name || '当前测速任务';
    $('bandwidthExportBtn').disabled = true;
    closeExportMenu('bandwidth');
    setSpeedRunning(true);
    await StartSpeedTest(settings.addr, settings.secs, selected.size ? [...selected].map(Number) : []);
};
$('speedBtn').addEventListener('click', async () => {
    if (speedRunning) {
        speedPaused = !speedPaused;
        if (speedPaused) await PauseSpeedTest();
        else await ResumeSpeedTest();
        setSpeedRunning(true);
        return;
    }
    // 静态确认弹窗：只显示一次性的任务信息，测速过程中不刷新
    const count = selected.size || nodes.length;
    $('taskNodeCount').textContent = String(count);
    $('taskSecs').textContent = String(settings.secs);
    $('taskEta').textContent = `约 ${count * settings.secs} 秒`;
    $('speedTaskModal').classList.remove('hidden');
});
$('taskStartBtn').addEventListener('click', () => {
    $('speedTaskModal').classList.add('hidden');
    startSpeedRun();
});
$('speedCancelBtn').addEventListener('click', async () => {
    await CancelSpeedTest();
    $('currentSpeedNode').textContent = '未开始';
});

EventsOn('speedNode', (ev) => {
    const key = String(ev.id);
    bandwidthResults.set(key, { ...(bandwidthResults.get(key) || {}), delay: ev.delay });
    const el = document.querySelector(`[data-bandwidth-delay="${key}"]`);
    if (el) {
        el.textContent = `${ev.delay}ms`;
        el.className = `delay-value ${delayTone(ev.delay)}`;
    }
    setRing(key, 100);
});

EventsOn('speedNodeProgress', (ev) => {
    setRing(String(ev.id), ev.progress * 100);
    if (ev.name) $('currentSpeedNode').textContent = ev.name;
});

const titlebar = $('titlebar');
let dragging = null;
let dragFrame = 0;

titlebar.addEventListener('mousedown', async (e) => {
    if (e.button !== 0 || e.target.closest('.tb-btn')) return;
    const p = await WindowGetPosition();
    dragging = { x: e.screenX - p.x, y: e.screenY - p.y };
});
window.addEventListener('mousemove', (e) => {
    if (!dragging || dragFrame) return;
    dragFrame = requestAnimationFrame(() => {
        dragFrame = 0;
        if (!dragging) return;
        WindowSetPosition(Math.round(e.screenX - dragging.x), Math.round(e.screenY - dragging.y));
    });
});
window.addEventListener('mouseup', () => { dragging = null; });
titlebar.addEventListener('dblclick', (e) => {
    if (!e.target.closest('.tb-btn')) WindowToggleMaximise();
});

// ---------- 节点数据 & 卡片渲染 ----------
let nodes = [];                       // 接口节点
let collapsed = { vip: false, user: false }; // 分组折叠状态
const results = new Map();            // 优选结果 nodeId -> delay(ms)
let currentFilter = 'all';
let preferNodeQuery = '';
let bandwidthNodeQuery = '';

const regionLabel = (china) => (china === 'yes' ? '国内' : '国外');
const delayTone = (delay) => delay <= 80 ? 'fast' : delay <= 150 ? 'medium' : 'slow';
const nodeMatchesQuery = (node, query) => {
    const q = query.trim().toLowerCase();
    return !q || String(node.id).toLowerCase().includes(q) || String(node.name || '').toLowerCase().includes(q);
};

function preferCardHTML(n) {
    const pill = n.group === 'vip'
        ? '<span class="pill vip">VIP</span>'
        : '<span class="pill user">普通</span>';
    const d = results.get(n.id);
    return `
        <div class="nc-top">
            <span class="nc-id">#${n.id}</span>
            <span>${pill}</span>
        </div>
        <div class="nc-name">${n.name}</div>
        <div class="prefer-metrics"><span>已发送 <b>暂无数据</b></span><span>已接收 <b>暂无数据</b></span><span>丢包率 <b>暂无数据</b></span><span>平均延迟 <b data-delay-value class="${d === undefined ? '' : `delay-value ${delayTone(d)}`}" >${d === undefined ? '--' : `${d}ms`}</b></span><span>下载速度 <b>暂无数据</b></span></div>
        <button class="prefer-detail-btn" type="button" data-prefer-id="${n.id}">详情</button>
    `;
}

function renderLeaderboard(boardId, listId, metaId, ranking) {
    const board = $(boardId);
    const list = $(listId);
    if (!board || !list || !ranking?.length) return;
    const top = ranking.slice(0, 3);
    const rest = ranking.slice(3, 5);
    const itemHTML = (item, index) => {
        const node = nodes.find((candidate) => candidate.id === item.id);
        return `
        <div class="ranking-item">
            <span class="ranking-rank">${index + 1}</span>
            <div class="ranking-name">${item.name}<span class="ranking-region">${node ? regionLabel(node.china) : '节点'} · #${item.id}</span></div>
            <span class="ranking-delay">${item.delay}<small> ms</small></span>
        </div>`;
    };
    list.innerHTML = top.map(itemHTML).join('') + (rest.length
        ? `<div class="ranking-rest">${rest.map((item, index) => itemHTML(item, index + 3)).join('')}</div>`
        : '');
    $(metaId).textContent = `共 ${ranking.length} 个节点 · 延迟越低越好`;
    board.classList.remove('hidden');
}

function renderPreferLeaderboard(ranking) {
    renderLeaderboard('preferLeaderboard', 'preferRankingList', 'preferLeaderboardMeta', ranking);
}

function bandwidthRanking() {
    const ranking = [];
    for (const [key, result] of bandwidthResults) {
        if (key === '__last__' || result?.delay === undefined) continue;
        const node = nodes.find((candidate) => String(candidate.id) === key);
        if (!node) continue;
        ranking.push({ id: node.id, name: node.name, delay: result.delay });
    }
    return ranking.sort((a, b) => a.delay - b.delay);
}

function hidePreferLeaderboard() {
    $('preferLeaderboard')?.classList.add('hidden');
}

function bandwidthCardHTML(n) {
    const pill = n.group === 'vip' ? '<span class="pill vip">VIP</span>' : '<span class="pill user">普通</span>';
    const result = bandwidthResults.get(String(n.id));
    const d = result?.delay;
    return `<div class="nc-top"><span class="nc-id">#${n.id}</span><span>${pill}</span></div><div class="nc-name">${n.name}</div><div class="bandwidth-metrics"><span>延迟 <b data-bandwidth-delay="${n.id}" class="${d === undefined ? '' : `delay-value ${delayTone(d)}`}">${d === undefined ? '--' : `${d}ms`}</b></span><span>下载 <b>${result?.down ?? '--'}</b></span><span>上传 <b>${result?.up ?? '--'}</b></span></div><svg class="nc-ring" viewBox="0 0 36 36" aria-hidden="true"><circle class="nc-ring-track" cx="18" cy="18" r="15.9" pathLength="100"/><circle class="nc-ring-fg" data-ring="${n.id}" cx="18" cy="18" r="15.9" pathLength="100" stroke-dasharray="100" stroke-dashoffset="100"/></svg><button class="node-detail-btn" type="button" data-test-id="${n.id}">详情</button>`;
}

function setRing(id, pct) {
    const circle = document.querySelector(`[data-ring="${id}"]`);
    if (!circle) return;
    const ring = circle.closest('.nc-ring');
    if (ring) ring.classList.add('active');
    circle.style.strokeDashoffset = String(100 - Math.max(0, Math.min(100, pct)));
}

function resetRings() {
    document.querySelectorAll('.nc-ring').forEach((ring) => ring.classList.remove('active'));
    document.querySelectorAll('[data-ring]').forEach((circle) => { circle.style.strokeDashoffset = '100'; });
}

function buildGroups() {
    const wrap = $('groups');
    wrap.innerHTML = '';
    const visibleNodes = nodes.filter((n) =>
        (currentFilter === 'all' || n.group === currentFilter) && nodeMatchesQuery(n, preferNodeQuery)
    );
    $('totalNodes').textContent = nodes.length || '--';
    $('selectedNodes').textContent = selected.size;
    $('filterCount').textContent = `${visibleNodes.length} 个节点`;
    const groups = [
        { key: 'vip', title: '会员节点', list: visibleNodes.filter((n) => n.group === 'vip') },
        { key: 'user', title: '普通节点', list: visibleNodes.filter((n) => n.group === 'user') },
    ];
    for (const g of groups) {
        if (g.list.length === 0) continue;
        const el = document.createElement('div');
        el.className = 'group' + (collapsed[g.key] ? ' collapsed' : '');
        el.innerHTML = `
            <div class="group-head">
                <span class="gh-title">${g.title}</span>
                <span class="gh-line"></span>
                <span class="gh-count" data-g="${g.key}">${g.list.length} 个节点 <span class="gh-arrow">▾</span></span>
            </div>
            <div class="cards"></div>
        `;
        const cards = el.querySelector('.cards');
        for (const n of g.list) {
            const c = document.createElement('div');
            c.className = 'node-card' + (selected.has(String(n.id)) ? ' selected' : '');
            c.dataset.id = String(n.id);
            c.innerHTML = preferCardHTML(n);
            c.addEventListener('click', () => toggleSelect(c, n));
            c.querySelector('.prefer-detail-btn').addEventListener('click', (event) => {
                event.stopPropagation();
                showPreferDetail(n);
            });
            cards.appendChild(c);
        }
        el.querySelector('.gh-count').addEventListener('click', () => {
            collapsed[g.key] = !collapsed[g.key];
            el.classList.toggle('collapsed', collapsed[g.key]);
        });
        wrap.appendChild(el);
    }
    updateSelUI();
    buildBandwidthGroups();
}

function buildBandwidthGroups() {
    const wrap = $('bandwidthGroups');
    if (!wrap) return;
    wrap.innerHTML = '';
    const filter = document.querySelector('.bandwidth-filter.active')?.dataset.filter || 'all';
    const visibleNodes = nodes.filter((n) =>
        (filter === 'all' || n.group === filter) && nodeMatchesQuery(n, bandwidthNodeQuery)
    );
    $('bandwidthTotal').textContent = nodes.length || '--';
    $('bandwidthSelected').textContent = selected.size;
    $('bandwidthFilterCount').textContent = `${visibleNodes.length} 个节点`;
    $('bandwidthSelectAllBtn').textContent = isAllSelected() ? '取消全选' : '全选';
    const groups = [
        { key: 'vip', title: '会员节点', list: visibleNodes.filter((n) => n.group === 'vip') },
        { key: 'user', title: '普通节点', list: visibleNodes.filter((n) => n.group === 'user') },
    ];
    for (const g of groups) {
        if (!g.list.length) continue;
        const el = document.createElement('div');
        el.className = 'group' + (collapsed[g.key] ? ' collapsed' : '');
        el.innerHTML = `<div class="group-head"><span class="gh-title">${g.title}</span><span class="gh-line"></span><span class="gh-count">${g.list.length} 个节点 <span class="gh-arrow">▾</span></span></div><div class="cards"></div>`;
        const cards = el.querySelector('.cards');
        g.list.forEach((n) => {
            const card = document.createElement('div');
            card.className = 'node-card' + (selected.has(String(n.id)) ? ' selected' : '');
            card.dataset.id = String(n.id);
            card.innerHTML = bandwidthCardHTML(n);
            card.addEventListener('click', () => toggleSelect(card, n));
            card.querySelector('.node-detail-btn').addEventListener('click', (event) => {
                event.stopPropagation();
                showBandwidthDetail(n);
            });
            cards.appendChild(card);
        });
        el.querySelector('.gh-count').addEventListener('click', () => {
            collapsed[g.key] = !collapsed[g.key];
            el.classList.toggle('collapsed', collapsed[g.key]);
        });
        wrap.appendChild(el);
    }
}

function showBandwidthDetail(node) {
    const result = bandwidthResults.get(String(node.id)) ?? bandwidthResults.get('__last__');
    $('bandwidthDetail').classList.remove('hidden');
    $('detailNodeName').textContent = node.name;
    $('detailDown').textContent = result?.down ? `${result.down} Mbps` : '--';
    $('detailUp').textContent = result?.up ? `${result.up} Mbps` : '--';
    $('detailAverage').textContent = result?.average ? `${result.average} Mbps` : '--';
    $('detailDuration').textContent = result?.duration ? `${result.duration} 秒` : '--';
}

function showPreferDetail(node) {
    const delay = results.get(node.id);
    $('preferDetail').classList.remove('hidden');
    $('preferDetailName').textContent = node.name;
    $('preferDetailRegion').textContent = `${regionLabel(node.china)} · #${node.id}`;
    $('preferDetailStatus').textContent = delay === undefined ? '尚未完成优选' : '优选已完成';
    $('preferDelay').textContent = delay === undefined ? '--' : `${delay} ms`;
}

function isAllSelected() {
    const all = nodes.map((n) => String(n.id));
    return all.length > 0 && all.every((id) => selected.has(id));
}

function updateSelUI() {
    $('selectAllBtn').textContent = isAllSelected() ? '取消全选' : '全选';
    $('selectedNodes').textContent = selected.size;
    if ($('bandwidthSelected')) $('bandwidthSelected').textContent = selected.size;
    if (preferRunning) return;
    if (selected.size === 0) {
        $('preferStatus').textContent = '点击卡片选择节点';
    } else if (selected.size === 1) {
        const id = [...selected][0];
        const n = nodes.find((x) => String(x.id) === id);
        $('preferStatus').innerHTML = `已选节点：<b>#${id} ${n ? n.name : ''}</b>`;
    } else {
        $('preferStatus').innerHTML = `已选 <b>${selected.size}</b> 个节点`;
    }
}

function toggleSelect(el, n) {
    const id = String(n.id);
    if (selected.has(id)) selected.delete(id);
    else selected.add(id);
    saveSel();
    el.classList.toggle('selected', selected.has(id));
    document.querySelectorAll(`.node-card[data-id="${id}"]`).forEach((card) => card.classList.toggle('selected', selected.has(id)));
    updateSelUI();
    buildBandwidthGroups();
}

$('selectAllBtn').addEventListener('click', () => {
    if (!nodes.length) return;
    if (isAllSelected()) selected.clear();
    else nodes.forEach((n) => selected.add(String(n.id)));
    saveSel();
    document.querySelectorAll('.node-card').forEach((c) =>
        c.classList.toggle('selected', selected.has(c.dataset.id)));
    updateSelUI();
    buildBandwidthGroups();
});

const cardById = (id) => document.querySelector(`.node-card[data-id="${id}"]`);

async function loadNodes() {
    $('groups').innerHTML = '<div class="empty-state"><span class="loader"></span><b>正在加载节点</b><small>请稍候</small></div>';
    $('bandwidthGroups').innerHTML = '<div class="empty-state"><span class="loader"></span><b>正在加载节点</b><small>请稍候</small></div>';
    $('preferStatus').textContent = '节点加载中…';
    try {
        nodes = await GetNodes('all');
    } catch (err) {
        $('groups').innerHTML = `<div class="empty-state"><b>节点加载失败</b><small>${err}</small></div>`;
        $('bandwidthGroups').innerHTML = `<div class="empty-state"><b>节点加载失败</b><small>${err}</small></div>`;
        $('preferStatus').textContent = '节点拉取失败';
        return;
    }
    // 清理已不存在的选中 id
    const valid = new Set(nodes.map((n) => String(n.id)));
    let pruned = false;
    for (const id of [...selected]) if (!valid.has(id)) { selected.delete(id); pruned = true; }
    if (pruned) saveSel();
    buildGroups();
}

document.querySelectorAll('#view-speed .filter').forEach((button) => {
    button.addEventListener('click', () => {
        currentFilter = button.dataset.filter;
        document.querySelectorAll('#view-speed .filter').forEach((item) => item.classList.toggle('active', item === button));
        buildGroups();
    });
});

document.querySelectorAll('.bandwidth-filter').forEach((button) => {
    button.addEventListener('click', () => {
        document.querySelectorAll('.bandwidth-filter').forEach((item) => item.classList.toggle('active', item === button));
        buildBandwidthGroups();
    });
});

$('preferNodeSearch').addEventListener('input', (event) => {
    preferNodeQuery = event.target.value;
    buildGroups();
});

$('bandwidthNodeSearch').addEventListener('input', (event) => {
    bandwidthNodeQuery = event.target.value;
    buildBandwidthGroups();
});

$('refreshBtn').addEventListener('click', () => {
    if (preferRunning) return;
    results.clear();
    loadNodes();
});
$('bandwidthRefreshBtn').addEventListener('click', () => {
    if (bandwidthRunning) return;
    loadNodes();
});
$('bandwidthSelectAllBtn').addEventListener('click', () => {
    if (!nodes.length) return;
    const all = nodes.every((n) => selected.has(String(n.id)));
    if (all) selected.clear();
    else nodes.forEach((n) => selected.add(String(n.id)));
    saveSel();
    $('bandwidthSelectAllBtn').textContent = all ? '全选' : '取消全选';
    buildGroups();
});

// ---------- 优选流（模拟，后端事件驱动；第二步接真实测速前端不改） ----------
let preferRunning = false;
let preferPaused = false;
let preferTotal = 0;

function setPreferBtn(running) {
    $('preferBtn').textContent = preferPaused ? '继续' : (running ? '暂停' : '开始优选');
    $('preferBtn').classList.toggle('speed-resume', preferPaused);
    $('preferCancelBtn').classList.toggle('hidden', !running);
}

EventsOn('preferEvent', (ev) => {
    switch (ev.kind) {
        case 'start':
            preferRunning = true;
            preferPaused = false;
            preferTotal = ev.total;
            results.clear();
            hidePreferLeaderboard();
            document.querySelectorAll('.node-card').forEach((c) => {
                c.classList.remove('testing');
                const d = c.querySelector('[data-delay]');
                if (d) d.remove();
                const delayValue = c.querySelector('[data-delay-value]');
                if (delayValue) {
                    delayValue.textContent = '--';
                    delayValue.className = '';
                }
            });
            setPreferBtn(true);
            $('refreshBtn').disabled = true;
            $('preferExportBtn').disabled = true;
            closeExportMenu('prefer');
            $('preferStatus').textContent = '准备优选';
            $('runState').textContent = '测试中';
            $('runStateHint').textContent = `0 / ${ev.total} 个节点`;
            $('preferProgressFill').style.width = '0%';
            break;

        case 'testing': {
            document.querySelectorAll('.node-card.testing').forEach((c) => c.classList.remove('testing'));
            const card = cardById(ev.nodeId);
            if (card) card.classList.add('testing');
            $('preferStatus').textContent = ev.name;
            $('runState').textContent = '测试中';
            $('runStateHint').textContent = `${ev.index} / ${ev.total} 个节点`;
            $('preferProgressFill').style.width = `${Math.round(ev.index / ev.total * 100)}%`;
            break;
        }

        case 'node': {
            results.set(ev.nodeId, ev.delay);
            const card = cardById(ev.nodeId);
            if (card) {
                card.classList.remove('testing');
                const delayValue = card.querySelector('[data-delay-value]');
                if (delayValue) {
                    delayValue.textContent = `${ev.delay}ms`;
                    delayValue.className = `delay-value ${delayTone(ev.delay)}`;
                }
            }
            if (!$('bestDelay').textContent || $('bestDelay').textContent === '-- ms') {
                $('bestDelay').innerHTML = `${ev.delay}<em> ms</em>`;
            } else {
                const current = parseFloat($('bestDelay').textContent);
                if (!Number.isNaN(current) && ev.delay < current) $('bestDelay').innerHTML = `${ev.delay}<em> ms</em>`;
            }
            break;
        }

        case 'finish': {
            preferRunning = false;
            preferPaused = false;
            setPreferBtn(false);
            $('refreshBtn').disabled = false;
            $('preferExportBtn').disabled = false;
            document.querySelectorAll('.node-card').forEach((c) => c.classList.remove('testing'));
            if (ev.result && ev.result.length) {
                renderPreferLeaderboard(ev.result);
                const order = new Map(ev.result.map((item, index) => [item.id, index]));
                nodes.sort((a, b) => (order.get(a.id) ?? Number.MAX_SAFE_INTEGER) - (order.get(b.id) ?? Number.MAX_SAFE_INTEGER));
                buildGroups();
                const best = ev.result[0];
                $('preferStatus').innerHTML =
                `优选完成 · 最快：<b>#${best.id} ${best.name}</b> <span class="ok">${best.delay}ms</span> · 共 ${ev.total} 个节点`;
                $('bestDelay').innerHTML = `${best.delay}<em> ms</em>`;
            } else {
                $('preferStatus').textContent = '优选完成';
            }
            $('runState').textContent = '已完成';
            $('runStateHint').textContent = `已测试 ${ev.total} 个节点`;
            $('preferProgressFill').style.width = '100%';
            break;
        }

        case 'cancel':
            preferRunning = false;
            preferPaused = false;
            setPreferBtn(false);
            $('refreshBtn').disabled = false;
            $('preferExportBtn').disabled = false;
            document.querySelectorAll('.node-card.testing').forEach((c) => c.classList.remove('testing'));
            $('preferStatus').textContent = '优选已取消';
            $('runState').textContent = '已取消';
            $('runStateHint').textContent = '可以重新开始优选';
            $('preferProgressFill').style.width = '0%';
            break;
    }
});

$('preferBtn').addEventListener('click', async () => {
    if (preferRunning) {
        preferPaused = !preferPaused;
        if (preferPaused) await PausePrefer();
        else await ResumePrefer();
        setPreferBtn(true);
        return;
    }
    if (!nodes.length) return;
    await StartPrefer(selected.size ? [...selected].map(Number) : []);
});
$('preferCancelBtn').addEventListener('click', async () => {
    await CancelPrefer();
});

// ---------- 导出（节点列表 + 优选结果）：CSV / 图片 ----------
const preferExportRows = () => {
    const sorted = [...results.entries()].sort((a, b) => a[1] - b[1]);
    const rank = new Map(sorted.map(([id], i) => [id, i + 1]));
    const rows = [['编号', '名称', '区域', '节点组', '地域', '建站', 'UDP', '防御', '延迟(ms)', '优选排名']];
    for (const n of nodes) {
        rows.push([
            n.id, n.name, n.area,
            n.group === 'vip' ? '会员' : '免费',
            n.china === 'yes' ? '国内' : '国外',
            n.web === 'yes' ? '是' : '否',
            n.udp ? '是' : '否',
            n.fangyu ? '是' : '否',
            results.has(n.id) ? results.get(n.id) : '',
            rank.has(n.id) ? rank.get(n.id) : '',
        ]);
    }
    return rows;
};

const exportPreferCsv = async () => {
    if (!nodes.length) return;
    const rows = preferExportRows();
    const csv = '\uFEFF' + rows.map((r) =>
        r.map((c) => `"${String(c).replace(/"/g, '""')}"`).join(',')
    ).join('\r\n');
    try {
        const path = await SaveCSV(csv);
        if (path) $('preferStatus').innerHTML = `已导出：<b>${path}</b>`;
    } catch (err) {
        $('preferStatus').textContent = '导出失败：' + err;
    }
};

const exportPreferPng = async () => {
    if (!nodes.length) return;
    const rows = preferExportRows();
    const w = 980;
    const h = 640;
    const canvas = document.createElement('canvas');
    canvas.width = w;
    canvas.height = h;
    const ctx = canvas.getContext('2d');
    const css = getComputedStyle(document.documentElement);
    const color = (name, fallback) => (css.getPropertyValue(name).trim() || fallback);
    ctx.fillStyle = color('--bg', '#0d0f12');
    ctx.fillRect(0, 0, w, h);
    ctx.fillStyle = color('--text', '#f2f4f7');
    ctx.font = '600 20px "Microsoft YaHei", sans-serif';
    ctx.fillText('ChmlFrp 节点优选报告', 36, 48);
    ctx.fillStyle = color('--muted', '#8b939e');
    ctx.font = '12px "Microsoft YaHei", sans-serif';
    const tested = [...results.keys()].length;
    ctx.fillText(`测速时间：${new Date().toLocaleString('zh-CN')}　节点数：${nodes.length}　已测：${tested}　延迟越低越好`, 36, 74);
    const cols = [36, 96, 330, 430, 510, 590, 670, 760, 850];
    const headers = ['编号', '名称', '区域', '节点组', '地域', 'UDP', '防御', '延迟', '排名'];
    ctx.fillStyle = color('--dim', '#5c6570');
    ctx.font = '11px "Microsoft YaHei", sans-serif';
    headers.forEach((t, i) => ctx.fillText(t, cols[i], 108));
    ctx.strokeStyle = color('--line', '#292e36');
    ctx.beginPath();
    ctx.moveTo(36, 116);
    ctx.lineTo(w - 36, 116);
    ctx.stroke();
    ctx.font = '12px "Microsoft YaHei", sans-serif';
    rows.slice(1, 13).forEach((r, idx) => {
        const y = 140 + idx * 22;
        ctx.fillStyle = color('--text', '#f2f4f7');
        const cell = [
            `#${r[0]}`, String(r[1]).slice(0, 18), String(r[2]).slice(0, 10), String(r[3]), String(r[4]),
            String(r[6]), String(r[7]),
            r[8] === '' ? '--' : `${r[8]} ms`, r[9] === '' ? '--' : `#${r[9]}`,
        ];
        cell.forEach((t, i) => ctx.fillText(t, cols[i], y));
    });
    // 延迟对比柱状图（取已测节点中延迟最低的前 10 个）
    const chartTop = 430;
    const chartH = 170;
    ctx.fillStyle = color('--dim', '#5c6570');
    ctx.font = '11px "Microsoft YaHei", sans-serif';
    ctx.fillText('延迟对比（ms，取延迟最低的前 10 个节点）', 36, chartTop - 12);
    ctx.strokeStyle = color('--line', '#292e36');
    ctx.strokeRect(36, chartTop, w - 72, chartH);
    const ranked = [...results.entries()].sort((a, b) => a[1] - b[1]).slice(0, 10);
    if (!ranked.length) {
        ctx.fillStyle = color('--muted', '#8b939e');
        ctx.font = '12px "Microsoft YaHei", sans-serif';
        ctx.fillText('暂无延迟数据，请先完成一次优选', 56, chartTop + chartH / 2);
    } else {
        const maxDelay = Math.max(...ranked.map(([, d]) => d), 1);
        const barW = (w - 112) / ranked.length;
        ranked.forEach(([id, delay], i) => {
            const node = nodes.find((n) => n.id === id);
            const barH = Math.max(4, (delay / maxDelay) * (chartH - 46));
            const x = 56 + i * barW;
            const y = chartTop + chartH - 24 - barH;
            ctx.fillStyle = delay <= 80 ? '#63d9a0' : delay <= 150 ? '#d9dde3' : '#ef8b8b';
            ctx.fillRect(x + 6, y, barW - 16, barH);
            ctx.fillStyle = color('--text', '#f2f4f7');
            ctx.font = '11px "Microsoft YaHei", sans-serif';
            ctx.fillText(`${delay}`, x + 6, y - 6);
            ctx.fillStyle = color('--dim', '#5c6570');
            ctx.font = '10px "Microsoft YaHei", sans-serif';
            ctx.fillText(String(node?.name ?? `#${id}`).slice(0, 8), x + 6, chartTop + chartH - 8);
        });
    }
    try {
        const path = await SaveImage(canvas.toDataURL('image/png').split(',')[1]);
        if (path) $('preferStatus').innerHTML = `已导出：<b>${path}</b>`;
    } catch (err) {
        $('preferStatus').textContent = '导出失败：' + err;
    }
};

// ---------- 节点列表页 ----------
function renderNodesTable() {
    const filter = $('groupFilter').value;
    const list = filter === 'all' ? nodes : nodes.filter((n) => n.group === filter);
    const tbody = $('nodesTable').querySelector('tbody');
    tbody.innerHTML = list.map((n) => `
        <tr>
            <td>#${n.id}</td><td>${n.name}</td><td>${n.area || '-'}</td>
            <td><span class="pill ${n.group === 'vip' ? 'vip' : 'user'}">${n.group === 'vip' ? '会员' : '普通'}</span></td>
            <td>${regionLabel(n.china)}</td><td><span class="pill ok">可用</span></td>
            <td>${results.has(n.id) ? `${results.get(n.id)} ms` : '--'}</td><td>待测速</td>
        </tr>`).join('');
}
$('refreshNodes').addEventListener('click', async () => {
    $('nodesMsg').classList.remove('hidden');
    $('nodesMsg').innerHTML = '<span class="loader"></span><b>正在加载节点</b><small>请稍候</small>';
    await loadNodes();
    $('nodesMsg').classList.add('hidden');
    $('nodesCard').classList.remove('hidden');
    renderNodesTable();
});
$('groupFilter').addEventListener('change', renderNodesTable);
$('closeBandwidthDetail').addEventListener('click', () => $('bandwidthDetail').classList.add('hidden'));

// ---------- 测速任务弹窗（静态确认信息，不做动态刷新） ----------
$('closeSpeedTask').addEventListener('click', () => $('speedTaskModal').classList.add('hidden'));
$('taskCloseBtn').addEventListener('click', () => $('speedTaskModal').classList.add('hidden'));

// ---------- 导出带宽测速结果（CSV / 图片） ----------
const bandwidthExportRows = () => {
    const ts = new Date().toLocaleString('zh-CN');
    const rows = [['编号', '名称', '节点组', '地域', '延迟(ms)', '下载(Mbps)', '上传(Mbps)', '平均(Mbps)', '测速时长(秒)', '测速时间']];
    for (const [key, r] of bandwidthResults) {
        if (key === '__last__' || !r) continue;
        if (r.delay === undefined && !r.down) continue;
        const n = nodes.find((candidate) => String(candidate.id) === key);
        if (!n) continue;
        rows.push([
            n.id, n.name,
            n.group === 'vip' ? '会员' : '普通',
            regionLabel(n.china),
            r.delay ?? '', r.down ?? '', r.up ?? '', r.average ?? '',
            r.duration ?? '', ts,
        ]);
    }
    return rows;
};
const toCsvText = (rows) => '﻿' + rows.map((r) =>
    r.map((c) => `"${String(c).replace(/"/g, '""')}"`).join(',')
).join('\r\n');

const exportBandwidthCsv = async () => {
    const rows = bandwidthExportRows();
    if (rows.length <= 1) return;
    try {
        const path = await SaveCSV(toCsvText(rows));
        if (path) $('speedState').textContent = '已导出 CSV';
    } catch (err) {
        $('speedState').textContent = '导出失败';
    }
};

const exportBandwidthPng = async () => {
    const rows = bandwidthExportRows();
    if (rows.length <= 1) return;
    const w = 980;
    const h = 640;
    const canvas = document.createElement('canvas');
    canvas.width = w;
    canvas.height = h;
    const ctx = canvas.getContext('2d');
    const css = getComputedStyle(document.documentElement);
    const color = (name, fallback) => (css.getPropertyValue(name).trim() || fallback);
    ctx.fillStyle = color('--bg', '#0d0f12');
    ctx.fillRect(0, 0, w, h);
    ctx.fillStyle = color('--text', '#f2f4f7');
    ctx.font = '600 20px "Microsoft YaHei", sans-serif';
    ctx.fillText('ChmlFrp 带宽测速报告', 36, 48);
    ctx.fillStyle = color('--muted', '#8b939e');
    ctx.font = '12px "Microsoft YaHei", sans-serif';
    ctx.fillText(`测速时间：${new Date().toLocaleString('zh-CN')}　节点数：${rows.length - 1}　单次时长：${settings.secs} 秒`, 36, 74);
    const cols = [36, 96, 330, 390, 470, 560, 650, 740];
    const headers = ['编号', '名称', '节点组', '地域', '延迟', '下载', '上传', '平均'];
    ctx.fillStyle = color('--dim', '#5c6570');
    ctx.font = '11px "Microsoft YaHei", sans-serif';
    headers.forEach((t, i) => ctx.fillText(t, cols[i], 108));
    ctx.strokeStyle = color('--line', '#292e36');
    ctx.beginPath();
    ctx.moveTo(36, 116);
    ctx.lineTo(w - 36, 116);
    ctx.stroke();
    ctx.font = '12px "Microsoft YaHei", sans-serif';
    rows.slice(1, 13).forEach((r, idx) => {
        const y = 140 + idx * 22;
        ctx.fillStyle = color('--text', '#f2f4f7');
        const cell = [
            `#${r[0]}`, String(r[1]).slice(0, 18), String(r[2]), String(r[3]),
            r[4] === '' ? '--' : `${r[4]} ms`, r[5] === '' ? '--' : `${r[5]} Mbps`,
            r[6] === '' ? '--' : `${r[6]} Mbps`, r[7] === '' ? '--' : `${r[7]} Mbps`,
        ];
        cell.forEach((t, i) => ctx.fillText(t, cols[i], y));
    });
    const chartTop = 430;
    const chartH = 160;
    ctx.fillStyle = color('--dim', '#5c6570');
    ctx.font = '11px "Microsoft YaHei", sans-serif';
    ctx.fillText('实时上下行速率（Mbps）', 36, chartTop - 12);
    ctx.fillText('下载', 760, chartTop - 12);
    ctx.fillStyle = '#e8eaed';
    ctx.fillRect(740, chartTop - 20, 14, 3);
    ctx.fillStyle = color('--dim', '#5c6570');
    ctx.fillText('上传', 850, chartTop - 12);
    ctx.fillStyle = '#8b939e';
    ctx.fillRect(830, chartTop - 20, 14, 3);
    ctx.strokeStyle = color('--line', '#292e36');
    ctx.strokeRect(36, chartTop, w - 72, chartH);
    if (speedSamples.length >= 2) {
        const list = speedSamples.slice(-64);
        const max = Math.max(...list.map((s) => Math.max(s.down, s.up)), 1);
        const px = (i) => 36 + (i / (list.length - 1)) * (w - 72);
        const py = (v) => chartTop + chartH - 10 - (v / max) * (chartH - 20);
        ['down', 'up'].forEach((key) => {
            ctx.strokeStyle = key === 'down' ? '#e8eaed' : '#8b939e';
            ctx.lineWidth = 1.6;
            ctx.beginPath();
            list.forEach((s, i) => {
                const x = px(i);
                const y = py(s[key]);
                if (i === 0) ctx.moveTo(x, y);
                else ctx.lineTo(x, y);
            });
            ctx.stroke();
        });
    } else {
        ctx.fillStyle = color('--muted', '#8b939e');
        ctx.fillText('暂无速率曲线数据', 36, chartTop + chartH / 2);
    }
    try {
        const path = await SaveImage(canvas.toDataURL('image/png').split(',')[1]);
        if (path) $('speedState').textContent = '已导出图片';
    } catch (err) {
        $('speedState').textContent = '导出失败';
    }
};

// ---------- 导出下拉菜单（节点优选 / 带宽测速共用） ----------
const exportMenus = {
    prefer: { btn: 'preferExportBtn', menu: 'preferExportDropdown', csv: exportPreferCsv, png: exportPreferPng },
    bandwidth: { btn: 'bandwidthExportBtn', menu: 'bandwidthExportDropdown', csv: exportBandwidthCsv, png: exportBandwidthPng },
};
const closeExportMenu = (key) => {
    const item = exportMenus[key];
    if (!item) return;
    $(item.menu).classList.add('hidden');
    $(item.btn).setAttribute('aria-expanded', 'false');
};
const closeAllExportMenus = () => Object.keys(exportMenus).forEach(closeExportMenu);
Object.entries(exportMenus).forEach(([key, item]) => {
    $(item.btn).addEventListener('click', (event) => {
        event.stopPropagation();
        const open = $(item.menu).classList.contains('hidden');
        closeAllExportMenus();
        if (open && !$(item.btn).disabled) {
            $(item.menu).classList.remove('hidden');
            $(item.btn).setAttribute('aria-expanded', 'true');
        }
    });
    $(item.menu).querySelectorAll('button[data-export]').forEach((option) => {
        option.addEventListener('click', async (event) => {
            event.stopPropagation();
            closeAllExportMenus();
            if ($(item.btn).disabled) return;
            if (option.dataset.export === 'csv') await item.csv();
            else await item.png();
        });
    });
});
document.addEventListener('click', () => closeAllExportMenus());

// ---------- 主题（跟随系统开关 + 手动暗/亮） ----------
const themeMode = {
    get v() { return localStorage.getItem('appTheme') || 'dark'; },
    set v(value) { localStorage.setItem('appTheme', value); },
};
const manualTheme = {
    get v() { return localStorage.getItem('appThemeManual') || 'dark'; },
    set v(value) { localStorage.setItem('appThemeManual', value); },
};
const themeMQ = window.matchMedia('(prefers-color-scheme: light)');
const applyTheme = () => {
    const mode = themeMode.v;
    const resolved = mode === 'system' ? (themeMQ.matches ? 'light' : 'dark') : mode;
    document.documentElement.dataset.theme = resolved;
};
const syncThemeUI = () => {
    const isSystem = themeMode.v === 'system';
    $('setThemeSystem').checked = isSystem;
    $('themeManualRow').classList.toggle('hidden', isSystem);
    document.querySelectorAll('.theme-btn').forEach((b) => b.classList.toggle('active', !isSystem && b.dataset.themeValue === themeMode.v));
};
themeMQ.addEventListener('change', () => { if (themeMode.v === 'system') applyTheme(); });
$('setThemeSystem').addEventListener('change', (e) => {
    themeMode.v = e.target.checked ? 'system' : manualTheme.v;
    syncThemeUI();
    applyTheme();
});
document.querySelectorAll('.theme-btn').forEach((btn) => {
    btn.addEventListener('click', () => {
        manualTheme.v = btn.dataset.themeValue;
        themeMode.v = btn.dataset.themeValue;
        syncThemeUI();
        applyTheme();
    });
});

// ---------- 开机自启 ----------
$('setAutostart').addEventListener('change', async (e) => {
    try {
        await SetAutostart(e.target.checked);
        $('autostartHint').textContent = e.target.checked ? '已开启开机自启' : '已关闭开机自启';
    } catch (err) {
        e.target.checked = !e.target.checked;
        $('autostartHint').textContent = String(err);
    }
});

$('closePreferDetail').addEventListener('click', () => $('preferDetail').classList.add('hidden'));
$('preferDetailBackdrop').addEventListener('click', () => $('preferDetail').classList.add('hidden'));

// ---------- 广告横幅（排行榜未完成时占位，点击跳转） ----------
const AD_URL = 'https://fanyi.qzhua.net/';
[['preferLeaderboard', 'preferAdBanner'], ['bandwidthLeaderboard', 'bandwidthAdBanner']].forEach(([boardId, adId]) => {
    const board = $(boardId);
    const ad = $(adId);
    if (!board || !ad) return;
    // 排行榜与广告互斥：监听排行榜 hidden 变化，隐藏时显示广告，显示时隐藏广告
    // dismissed：用户点 × 后本次会话内不再显示
    let dismissed = false;
    const syncAd = () => ad.classList.toggle('hidden', dismissed || !board.classList.contains('hidden'));
    new MutationObserver(syncAd).observe(board, { attributes: true, attributeFilter: ['class'] });
    syncAd();
    ad.querySelector('.ad-close')?.addEventListener('click', (e) => {
        e.stopPropagation();
        dismissed = true;
        syncAd();
    });
    const openAd = () => BrowserOpenURL(AD_URL);
    ad.addEventListener('click', openAd);
    ad.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            openAd();
        }
    });
});

// ---------- 启动 ----------
applyTheme();
syncThemeUI();
GetAutostart().then((on) => { $('setAutostart').checked = on; }).catch((err) => {
    $('setAutostart').disabled = true;
    $('autostartHint').textContent = String(err);
});
loadNodes();
