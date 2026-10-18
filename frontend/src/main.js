import './style.css';
import { GetNodes, StartPrefer, CancelPrefer, PausePrefer, ResumePrefer, SaveCSV, SaveImage, StartSpeedTest, CancelSpeedTest, PauseSpeedTest, ResumeSpeedTest, GetAutostart, SetAutostart, HideToTray, GetLocalIPs, GetMyIPInfo, QueryIPInfo } from '../wailsjs/go/main/App';
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
        if (btn.dataset.view === 'tools') ensureToolsLoaded();
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

// ---------- 运行日志（延迟测速 + 带宽测速共用） ----------
const runLogs = []; // { source: 'prefer'|'bandwidth', level: 'info'|'ok'|'warn'|'err', msg, time }
let logFilterSource = 'all';
let logQuery = '';
let speedRunning = false;

const pad2 = (n) => String(n).padStart(2, '0');
const logTime = () => {
    const d = new Date();
    return `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
};
const logSourceLabel = (source) => (source === 'prefer' ? '延迟' : '带宽');
const logLevelTag = (level) => (level === 'ok' ? 'OK' : level === 'err' ? 'ERR' : level === 'warn' ? 'WARN' : 'INFO');

const updateLogCounts = () => {
    const ok = runLogs.filter((x) => x.level === 'ok').length;
    const err = runLogs.filter((x) => x.level === 'err').length;
    const warn = runLogs.filter((x) => x.level === 'warn').length;
    if ($('logTotalCount')) $('logTotalCount').textContent = String(runLogs.length);
    if ($('logOkCount')) $('logOkCount').textContent = String(ok);
    if ($('logErrCount')) $('logErrCount').textContent = String(err);
    if ($('logWarnCount')) $('logWarnCount').textContent = String(warn);
};

const renderLogs = () => {
    const list = $('logList');
    if (!list) return;
    const rows = runLogs.filter((x) =>
        (logFilterSource === 'all' || x.source === logFilterSource)
        && (!logQuery || x.msg.toLowerCase().includes(logQuery))
    );
    if ($('logFilterCount')) $('logFilterCount').textContent = `${rows.length} 条`;
    if (!rows.length) {
        list.innerHTML = `<div class="log-empty"><b>暂无日志</b><small>${runLogs.length ? '当前筛选条件下没有匹配记录' : '执行延迟测速或带宽测速后，记录会显示在这里'}</small></div>`;
        return;
    }
    list.innerHTML = rows.map((x) => `
        <div class="log-line">
            <span class="log-time">${x.time}</span>
            <span class="log-source ${x.source}">${logSourceLabel(x.source)}</span>
            <span class="log-tag ${x.level}">${logLevelTag(x.level)}</span>
            <span class="log-msg"></span>
        </div>`).join('');
    // 消息用 textContent 写入，避免节点名等文本注入 HTML
    const msgEls = list.querySelectorAll('.log-msg');
    rows.forEach((x, i) => { if (msgEls[i]) msgEls[i].textContent = x.msg; });
    if ($('logAutoScroll')?.checked) list.scrollTop = list.scrollHeight;
};

// 日志重绘按帧合并：appendLog 每条都全量重绘最多 1000 行 DOM，
// 优选/测速时事件密集到来（几十上百条），逐条重绘会明显掉帧。
let logRenderPending = false;
const scheduleLogRender = () => {
    if (logRenderPending) return;
    logRenderPending = true;
    requestAnimationFrame(() => {
        logRenderPending = false;
        updateLogCounts();
        renderLogs();
    });
};

const appendLog = (source, level, msg) => {
    runLogs.push({ source, level, msg, time: logTime() });
    if (runLogs.length > 1000) runLogs.shift();
    scheduleLogRender();
};
// 带宽测速的快捷写法
const appendSpeedLog = (level, msg) => appendLog('bandwidth', level, msg);

document.querySelectorAll('.log-filter').forEach((btn) => {
    btn.addEventListener('click', () => {
        document.querySelectorAll('.log-filter').forEach((b) => b.classList.toggle('active', b === btn));
        logFilterSource = btn.dataset.logFilter;
        renderLogs();
    });
});
$('logSearch')?.addEventListener('input', (e) => {
    logQuery = e.target.value.trim().toLowerCase();
    renderLogs();
});
$('logClearBtn')?.addEventListener('click', () => {
    runLogs.length = 0;
    updateLogCounts();
    renderLogs();
});
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
    $('speedState').textContent = speedPaused ? '已暂停' : (running ? '测速中' : '待测速');
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
    if (ev.cancelled) {
        appendSpeedLog('warn', `测速已停止 · 地址 ${ev.addr}`);
    } else {
        appendSpeedLog('ok', `测速完成 · 下载 ${formatSpeed(ev.downAvg)} Mbps · 上传 ${formatSpeed(ev.upAvg)} Mbps · 峰值 ${formatSpeed(ev.downPeak)}/${formatSpeed(ev.upPeak)}`);
    }
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
    appendSpeedLog('info', `开始测速：${currentNode?.name || '未选择节点'} · 时长 ${settings.secs}s · 地址 ${settings.addr}`);
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
        appendSpeedLog('info', speedPaused ? '测速已暂停' : '测速已恢复');
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
    appendSpeedLog('warn', '用户点击停止测速');
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
    const tone = ev.delay <= 80 ? '成功' : ev.delay <= 150 ? '正常' : '较慢';
    appendSpeedLog('ok', `#${ev.id} ${ev.name} · 延迟 ${ev.delay}ms · ${tone}`);
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
const results = new Map();            // 延迟测速结果 nodeId -> delay(ms)
const preferStats = new Map();        // 延迟测速 TCPing 统计 nodeId -> {sent, recv, loss, port}
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
    const delayText = d === undefined ? '--' : (d < 0 ? '超时' : `${d}ms`);
    const delayClass = d === undefined ? '' : `delay-value ${d < 0 ? 'slow' : delayTone(d)}`;
    // TCPing 统计：未测过显示 --，测过显示真实次数/丢包率
    const st = preferStats.get(n.id);
    const sentText = st ? `${st.sent} 次` : '--';
    const recvText = st ? `${st.recv} 次` : '--';
    const lossText = st ? `${Math.round(st.loss * 100)}%` : '--';
    return `
        <div class="nc-top">
            <span class="nc-id">#${n.id}</span>
            <span>${pill}</span>
        </div>
        <div class="nc-name">${n.name}</div>
        <div class="prefer-metrics"><span>已发送 <b data-stat="sent">${sentText}</b></span><span>已接收 <b data-stat="recv">${recvText}</b></span><span>丢包率 <b data-stat="loss">${lossText}</b></span><span>平均延迟 <b data-delay-value class="${delayClass}" >${delayText}</b></span></div>
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
    const timedOut = delay !== undefined && delay < 0;
    const st = preferStats.get(node.id);
    $('preferDetail').classList.remove('hidden');
    $('preferDetailName').textContent = node.name;
    $('preferDetailRegion').textContent = `${regionLabel(node.china)} · #${node.id}`;
    $('preferDetailStatus').textContent = delay === undefined
        ? '尚未完成测速'
        : (timedOut ? '所有端口不可达' : (st?.port ? `测速已完成 · 端口 ${st.port}` : '测速已完成'));
    $('preferDelay').textContent = delay === undefined ? '--' : (timedOut ? '超时' : `${delay} ms`);
    $('preferSent').textContent = st ? `${st.sent} 次` : '暂无数据';
    $('preferReceived').textContent = st ? `${st.recv} 次` : '暂无数据';
    $('preferLoss').textContent = st ? `${Math.round(st.loss * 100)}%` : '暂无数据';
    $('preferPort').textContent = st?.port ? String(st.port) : '暂无数据';
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

// ---------- 延迟测速流（后端事件驱动：TCPing 粗筛） ----------
let preferRunning = false;
let preferPaused = false;
let preferTotal = 0;

function setPreferBtn(running) {
    $('preferBtn').textContent = preferPaused ? '继续' : (running ? '暂停' : '开始测速');
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
            preferStats.clear();
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
                ['sent', 'recv', 'loss'].forEach((key) => {
                    const el = c.querySelector(`[data-stat="${key}"]`);
                    if (el) el.textContent = '--';
                });
            });
            setPreferBtn(true);
            $('refreshBtn').disabled = true;
            $('preferExportBtn').disabled = true;
            closeExportMenu('prefer');
            $('preferStatus').textContent = '准备测速';
            $('runState').textContent = '测试中';
            $('runStateHint').textContent = `0 / ${ev.total} 个节点`;
            $('preferProgressFill').style.width = '0%';
            appendLog('prefer', 'info', `开始测速 · 共 ${ev.total} 个节点`);
            break;

        case 'testing': {
            document.querySelectorAll('.node-card.testing').forEach((c) => c.classList.remove('testing'));
            const card = cardById(ev.nodeId);
            if (card) card.classList.add('testing');
            $('preferStatus').textContent = `测试中 · ${ev.name}`;
            break;
        }

        case 'node': {
            results.set(ev.nodeId, ev.delay);
            preferStats.set(ev.nodeId, { sent: ev.sent, recv: ev.recv, loss: ev.loss, port: ev.port });
            $('preferProgressFill').style.width = `${Math.round(ev.index / ev.total * 100)}%`;
            // 暂停时仍可能有已在途的探测结果回来，别把「已暂停」冲掉
            if (!preferPaused) $('runState').textContent = '测试中';
            $('runStateHint').textContent = `${ev.index} / ${ev.total} 个节点`;
            $('preferStatus').textContent = `${ev.name} · ${ev.delay < 0 ? '超时' : `${ev.delay}ms`}`;
            const card = cardById(ev.nodeId);
            if (card) {
                card.classList.remove('testing');
                const delayValue = card.querySelector('[data-delay-value]');
                if (delayValue) {
                    delayValue.textContent = ev.delay < 0 ? '超时' : `${ev.delay}ms`;
                    delayValue.className = `delay-value ${ev.delay < 0 ? 'slow' : delayTone(ev.delay)}`;
                }
                // TCPing 统计同步上卡
                const setStat = (key, text) => {
                    const el = card.querySelector(`[data-stat="${key}"]`);
                    if (el) el.textContent = text;
                };
                setStat('sent', `${ev.sent} 次`);
                setStat('recv', `${ev.recv} 次`);
                setStat('loss', `${Math.round(ev.loss * 100)}%`);
            }
            if (ev.delay >= 0) {
                if (!$('bestDelay').textContent || $('bestDelay').textContent === '-- ms') {
                    $('bestDelay').innerHTML = `${ev.delay}<em> ms</em>`;
                } else {
                    const current = parseFloat($('bestDelay').textContent);
                    if (!Number.isNaN(current) && ev.delay < current) $('bestDelay').innerHTML = `${ev.delay}<em> ms</em>`;
                }
                appendLog('prefer', 'ok', `#${ev.nodeId} ${ev.name} · 延迟 ${ev.delay}ms · 端口 ${ev.port}`);
            } else {
                appendLog('prefer', 'err', `#${ev.nodeId} ${ev.name} · 超时（7000/80/81 端口均不可达，节点可能未运行 frps）`);
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
                `测速完成 · 最快：<b>#${best.id} ${best.name}</b> <span class="ok">${best.delay}ms</span> · 共 ${ev.total} 个节点`;
                $('bestDelay').innerHTML = `${best.delay}<em> ms</em>`;
                appendLog('prefer', 'ok', `测速完成 · ${ev.result.length}/${ev.total} 个节点可用 · 最快 #${best.id} ${best.name} ${best.delay}ms`);
            } else {
                $('preferStatus').textContent = '测速完成';
                appendLog('prefer', 'warn', `测速完成 · ${ev.total} 个节点全部超时，无可用结果`);
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
            $('preferStatus').textContent = '测速已取消';
            $('runState').textContent = '已取消';
            $('runStateHint').textContent = '可以重新开始测速';
            $('preferProgressFill').style.width = '0%';
            appendLog('prefer', 'warn', '测速已取消');
            break;
    }
});

$('preferBtn').addEventListener('click', async () => {
    if (preferRunning) {
        preferPaused = !preferPaused;
        if (preferPaused) await PausePrefer();
        else await ResumePrefer();
        setPreferBtn(true);
        $('runState').textContent = preferPaused ? '已暂停' : '测试中';
        $('preferStatus').textContent = preferPaused ? '已暂停，点击「继续」恢复' : '已恢复，继续测速';
        appendLog('prefer', 'info', preferPaused ? '测速已暂停' : '测速已恢复');
        return;
    }
    if (!nodes.length) return;
    appendLog('prefer', 'info', selected.size ? `准备测速已选 ${selected.size} 个节点` : '准备测速全部节点');
    await StartPrefer(selected.size ? [...selected].map(Number) : []);
});
$('preferCancelBtn').addEventListener('click', async () => {
    appendLog('prefer', 'warn', '用户点击停止测速');
    await CancelPrefer();
});

// ---------- 导出（节点列表 + 延迟测速结果）：CSV / 图片 ----------
const preferExportRows = () => {
    // 只让「测到了延迟」的节点参与排名：超时节点在 results 里是 -1，
    // 若直接排序会拿到第 1 名（-1 最小），导出表就出现"-1ms 排名 1"的假数据。
    const sorted = [...results.entries()].filter(([, d]) => d >= 0).sort((a, b) => a[1] - b[1]);
    const rank = new Map(sorted.map(([id], i) => [id, i + 1]));
    const rows = [['编号', '名称', '区域', '节点组', '地域', '建站', 'UDP', '防御', '延迟(ms)', '延迟排名']];
    for (const n of nodes) {
        const d = results.get(n.id);
        rows.push([
            n.id, n.name, n.area,
            n.group === 'vip' ? '会员' : '免费',
            n.china === 'yes' ? '国内' : '国外',
            n.web === 'yes' ? '是' : '否',
            n.udp ? '是' : '否',
            n.fangyu ? '是' : '否',
            d === undefined ? '' : (d < 0 ? '超时' : d),
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
    ctx.fillText('ChmlFrp 延迟测速报告', 36, 48);
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
    // 只取真正测到延迟的节点：超时节点记作 -1，不过滤会排在队首、画出 -1 的柱子
    const ranked = [...results.entries()].filter(([, d]) => d >= 0).sort((a, b) => a[1] - b[1]).slice(0, 10);
    if (!ranked.length) {
        ctx.fillStyle = color('--muted', '#8b939e');
        ctx.font = '12px "Microsoft YaHei", sans-serif';
        ctx.fillText('暂无延迟数据，请先完成一次测速', 56, chartTop + chartH / 2);
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
    tbody.innerHTML = list.map((n) => {
        const d = results.get(n.id);
        const delayCell = d === undefined ? '--' : (d < 0 ? '超时' : `${d} ms`);
        return `
        <tr>
            <td>#${n.id}</td><td>${n.name}</td><td>${n.area || '-'}</td>
            <td><span class="pill ${n.group === 'vip' ? 'vip' : 'user'}">${n.group === 'vip' ? '会员' : '普通'}</span></td>
            <td>${regionLabel(n.china)}</td><td><span class="pill ok">可用</span></td>
            <td>${delayCell}</td><td>待测速</td>
        </tr>`;
    }).join('');
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

// ---------- 导出下拉菜单（延迟测速 / 带宽测速共用） ----------
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

// ---------- 本机 IP：网卡地址枚举 + 公网 IP 信息（ip-api.com） ----------
let toolsLoaded = false;

const copyText = async (text) => {
    try {
        await navigator.clipboard.writeText(text);
        return true;
    } catch {
        // WebView 里剪贴板可能受权限限制，退回到临时 textarea
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        let ok = false;
        try { ok = document.execCommand('copy'); } catch { ok = false; }
        ta.remove();
        return ok;
    }
};

// 回环与内网地址没有公网归属地，v4/v6 公网地址都可查
const ipLookupable = (x) => !x.loopback && !(x.version === 4 && x.private);
const ipTagText = (x) => (x.loopback ? '回环' : x.version === 6 ? 'IPv6' : (x.private ? '内网' : '公网'));
const ipTagClass = (x) => (x.loopback ? 'loop' : x.version === 6 ? 'v6' : (x.private ? 'lan' : 'wan'));

function renderLocalIPs(ips) {
    const list = $('localIpList');
    $('toolIpv4Count').textContent = ips.filter((x) => x.version === 4).length;
    $('toolIpv6Count').textContent = ips.filter((x) => x.version === 6).length;
    $('toolsAddrCount').textContent = `${ips.length} 个地址`;
    if (!ips.length) {
        list.innerHTML = '<div class="empty-state compact"><b>没有可用地址</b><small>网卡可能全部未启用</small></div>';
        return;
    }
    list.innerHTML = ips.map((x) => {
        const canLookup = ipLookupable(x);
        return `
        <div class="ip-row" data-ip="${x.ip}" data-lookup="${canLookup ? '1' : '0'}">
            <div class="ip-main"><b>${x.ip}</b><small>${x.iface} · IPv${x.version}</small></div>
            <span class="ip-tag ${ipTagClass(x)}">${ipTagText(x)}</span>
            <div class="ip-actions">
                <button class="ip-btn copy" type="button">复制</button>
                <button class="ip-btn lookup" type="button" ${canLookup ? '' : 'disabled title="内网/回环地址没有公网归属地"'}>归属地</button>
            </div>
        </div>`;
    }).join('');
    list.querySelectorAll('.ip-row').forEach((row) => {
        const ip = row.dataset.ip;
        row.querySelector('.copy').addEventListener('click', async () => {
            const btn = row.querySelector('.copy');
            const ok = await copyText(ip);
            btn.textContent = ok ? '已复制' : '复制失败';
            setTimeout(() => { btn.textContent = '复制'; }, 1200);
        });
        if (row.dataset.lookup === '1') {
            row.querySelector('.lookup').addEventListener('click', () => {
                $('ipQueryInput').value = ip;
                queryIP(ip);
            });
        }
    });
}

async function refreshLocalIPs() {
    $('localIpList').innerHTML = '<div class="empty-state compact"><span class="loader"></span><b>正在读取网卡</b></div>';
    try {
        renderLocalIPs(await GetLocalIPs());
    } catch (err) {
        $('localIpList').innerHTML = `<div class="empty-state compact"><b>读取网卡失败</b><small>${String(err)}</small></div>`;
    }
}

// 渲染 IP 信息卡片（self=true 表示当前出口 IP，否则为手动查询结果）
function renderIPGeo(geo, self) {
    const card = $('ipInfoCard');
    const place = [geo.region, geo.city].filter(Boolean).join(' · ') || '地理位置未知';
    // ip-api 的 as 字段形如 "AS9808 China Mobile Communications Group."，只取 AS 号
    const asMatch = String(geo.as || '').match(/AS\d+/i);
    const asn = asMatch ? asMatch[0].toUpperCase() : '--';
    const coord = (geo.latitude || geo.longitude)
        ? `${Number(geo.latitude).toFixed(4)}, ${Number(geo.longitude).toFixed(4)}`
        : '--';
    const cells = [
        ['国家/地区', geo.country ? `${geo.country}${geo.countryCode ? ` (${geo.countryCode})` : ''}` : '--'],
        ['省 / 州', geo.region || '--'],
        ['城市', geo.city || '--'],
        ['邮编', geo.zip || '--'],
        ['ASN', asn],
        ['运营商', geo.isp || '--', geo.org && geo.org !== geo.isp ? geo.org : ''],
        ['时区', geo.timeZone || '--'],
        ['经纬度', coord],
    ];
    const badge = geo.country ? `<span class="ip-badge">${geo.country}</span>` : '';
    card.className = 'ip-geo has-data';
    card.innerHTML = `
        <div class="ip-geo-head">
            <div class="ip-geo-title"><b>${geo.ip}</b>${badge}</div>
            <div class="ip-geo-sub"><span>${place}</span><span class="ip-geo-kind">IPv${geo.version || 4}${self ? ' · 当前出口' : ' · 指定查询'}</span></div>
        </div>
        <div class="ip-geo-grid">${cells.map(([k, v, extra]) => `<div><span>${k}</span><b>${v}</b>${extra ? `<em>${extra}</em>` : ''}</div>`).join('')}</div>
        <p class="ip-geo-note">数据来源 ip-api.com · 免密钥，约 45 次/分钟 · 中文结果</p>`;
    $('myIpBackBtn').classList.toggle('hidden', self);
}

function renderIPGeoLoading(text) {
    const card = $('ipInfoCard');
    card.className = 'ip-geo is-loading';
    card.innerHTML = `<div class="ip-geo-empty"><span class="loader"></span><b>${text}</b></div>`;
}

function renderIPGeoError(title, msg) {
    const card = $('ipInfoCard');
    card.className = 'ip-geo has-error';
    card.innerHTML = `<div class="ip-geo-empty"><b>${title}</b><small>${msg}</small></div>`;
}

const stripErr = (err) => String(err).replace(/^Error:\s*/, '');

// 自动获取当前出口 IP 信息
async function loadMyIP() {
    renderIPGeoLoading('正在获取当前 IP 信息');
    $('toolQueryState').textContent = '获取中…';
    try {
        renderIPGeo(await GetMyIPInfo(), true);
        $('toolQueryState').textContent = '已获取';
    } catch (err) {
        renderIPGeoError('获取当前 IP 失败', stripErr(err));
        $('toolQueryState').textContent = '获取失败';
    }
}

async function queryIP(inputIP) {
    const ip = String(inputIP ?? $('ipQueryInput').value).trim();
    if (!ip) {
        $('toolQueryState').textContent = '请输入 IP';
        return;
    }
    const btn = $('ipQueryBtn');
    btn.disabled = true;
    $('toolQueryState').textContent = '查询中…';
    renderIPGeoLoading(`正在查询 ${ip}`);
    try {
        renderIPGeo(await QueryIPInfo(ip), false);
        $('toolQueryState').textContent = '已查询';
    } catch (err) {
        renderIPGeoError('查询失败', stripErr(err));
        $('myIpBackBtn').classList.remove('hidden');
        $('toolQueryState').textContent = '查询失败';
    } finally {
        btn.disabled = false;
    }
}

async function ensureToolsLoaded() {
    if (toolsLoaded) return;
    toolsLoaded = true;
    await Promise.all([refreshLocalIPs(), loadMyIP()]);
}

$('toolsRefreshBtn').addEventListener('click', () => {
    refreshLocalIPs();
    loadMyIP();
});
$('myIpRefreshBtn').addEventListener('click', loadMyIP);
$('myIpBackBtn').addEventListener('click', loadMyIP);
$('ipQueryBtn').addEventListener('click', () => queryIP());
$('ipQueryInput').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') queryIP();
});

// ---------- 启动 ----------
applyTheme();
syncThemeUI();
GetAutostart().then((on) => { $('setAutostart').checked = on; }).catch((err) => {
    $('setAutostart').disabled = true;
    $('autostartHint').textContent = String(err);
});
loadNodes();
