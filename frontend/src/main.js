import './style.css';
import { ManageFrpcCache, IsFrpcReady, PrepareFrpc } from '../wailsjs/go/main/App';
import { GetNodes, StartPrefer, CancelPrefer, SaveCSV, SaveImage, StartSpeedTest, CancelSpeedTest, PauseSpeedTest, ResumeSpeedTest, GetAutostart, SetAutostart, HideToTray, QuitApp, GetLocalIPs, GetMyIPInfo, QueryIPInfo, RequestClose, SetCloseAction, GetSettings, SaveSettings, AppendRunLog, ExportRunLog, GetRunLogPath } from '../wailsjs/go/main/App';
import { EventsOn, WindowMinimise, WindowToggleMaximise, WindowGetPosition, WindowSetPosition, BrowserOpenURL } from '../wailsjs/runtime';
import { StartQZhuaLogin, LogoutQZhua, CancelQZhuaLogin, RestoreSession, SetRememberLogin } from '../wailsjs/go/main/App';
import { CheckForUpdate, StartUpdateDownload, CancelUpdateDownload, InstallUpdateAndRestart, SkipUpdateVersion, GetUpdatePrefs, GetUpdateState, SetAutoUpdateCheck } from '../wailsjs/go/main/App';
import { PingMCServer } from '../wailsjs/go/main/App';

const $ = (id) => document.getElementById(id);

// ---------- 后端事件登记表 ----------
// Wails 的 EventsOn 不会自动回收：页面里注册的回调会一直挂在运行时上。
// 这里统一登记解绑函数，退出前一次性注销，避免回调继续操作已销毁的 DOM。
const appEventUnsubs = [];
const adObservers = [];

function onAppEvent(name, handler) {
    const off = EventsOn(name, handler);
    if (typeof off === 'function') appEventUnsubs.push(off);
    return off;
}

function disposeAppEvents() {
    while (appEventUnsubs.length) {
        try { appEventUnsubs.pop()(); } catch { /* 解绑失败不该阻塞退出 */ }
    }
    adObservers.forEach((observer) => observer.disconnect());
    adObservers.length = 0;
}

onAppEvent('frpcSecurityNotice', (ev) => {
    $('frpcSecurityTitle').textContent = `${ev.stage || 'frpc'}：文件访问受阻`;
    $('frpcSecurityMessage').textContent = ev.message || '请查看 Windows 安全中心的保护历史。';
    $('frpcSecurityPath').textContent = ev.path || '无法确定路径';
    $('frpcSecurityModal').classList.remove('hidden');
});
$('frpcSecurityOK').addEventListener('click', () => $('frpcSecurityModal').classList.add('hidden'));
$('frpcSecuritySettings').addEventListener('click', () => BrowserOpenURL('windowsdefender:'));
$('frpcSecuritySubmit').addEventListener('click', () => BrowserOpenURL('https://www.microsoft.com/en-us/wdsi/filesubmission'));

function setTestButtonState(btn, label, loading, testing = false) {
    btn.querySelector('.test-button-label').textContent = label;
    btn.classList.toggle('is-loading', loading);
    btn.classList.toggle('is-testing', testing);
    btn.setAttribute('aria-busy', String(loading));
}

['preferBtn', 'speedBtn'].forEach((id) => {
    const btn = $(id);
    const label = document.createElement('span');
    label.className = 'test-button-label';
    label.textContent = btn.textContent;
    btn.classList.add('test-button');
    btn.innerHTML = '<span class="test-button-icon" aria-hidden="true"><svg class="test-button-ring" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/></svg><svg class="test-button-bolt" viewBox="0 0 24 24"><path d="M13 2 4 14h7l-1 8 10-12h-7l1-8Z"/></svg></span>';
    btn.append(label);
    btn.setAttribute('aria-busy', 'false');
});

// 远端数据（节点名、接口错误串等）进 innerHTML 之前一律转义。
// 节点名来自服务端接口，而页面里的脚本能直接调 Wails 绑定的后端方法，
// 一旦拼进未转义的 HTML 就等于给自己开了一条 XSS 通道（<img onerror=...> 可执行）。
const ESC_MAP = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
const esc = (value) => String(value ?? '').replace(/[&<>"']/g, (ch) => ESC_MAP[ch]);

// 搜索框逐字触发全量重建太浪费：按键停 150ms 再渲染，输入时的手感不受影响。
function debounce(fn, ms = 150) {
    let timer = 0;
    return (...args) => {
        clearTimeout(timer);
        timer = setTimeout(() => fn(...args), ms);
    };
}

// 弹窗必须位于页面根部，才能越过内容区的层叠上下文并遮住标题栏和侧栏。
document.querySelectorAll('.bandwidth-modal').forEach((modal) => document.body.append(modal));

// ---------- 设置（Go settings.json 的运行时快照） ----------
// 测速参数不在 localStorage 里存第二份：后端启动时读 settings.json，
// 设置页保存后返回实际生效值（可能被后端 clamp）。
const settings = { testSeconds: 10, bwParallel: 6, bwTunnelConcurrency: 2 };
const clampInt = (value, min, max, fallback) => {
    const n = Number.parseInt(value, 10);
    if (!Number.isFinite(n)) return Math.min(max, Math.max(min, fallback));
    return Math.min(max, Math.max(min, n));
};
// ---------- localStorage 安全读写 ----------
// 本地缓存属于不可信输入：手动改坏、旧版本残留、隐私模式都可能让读取抛异常。
// 这段代码在初始化阶段执行，一旦抛错整个 main.js 都不会继续加载，所以统一兜底。
const readStorage = (key) => {
    try {
        return localStorage.getItem(key);
    } catch {
        return null;
    }
};
const writeStorage = (key, value) => {
    try {
        localStorage.setItem(key, value);
    } catch { /* 隐私模式/配额满：写不进去就算了 */ }
};
const removeStorage = (key) => {
    try {
        localStorage.removeItem(key);
    } catch { /* 同上 */ }
};
const readStorageJson = (key, fallback = null) => {
    const raw = readStorage(key);
    if (!raw) return fallback;
    try {
        return JSON.parse(raw);
    } catch {
        return fallback;
    }
};
const readStorageArray = (key) => {
    const value = readStorageJson(key, []);
    return Array.isArray(value) ? value : [];
};

// 已选卡片（多选，点击卡片切换，全选按钮批量，持久化）
const selected = new Set(readStorageArray('pickedNodeIds').map(String));
const saveSel = () => writeStorage('pickedNodeIds', JSON.stringify([...selected]));
const bandwidthResults = new Map();

// ---------- 导航 ----------
document.querySelectorAll('.nav-item').forEach((btn) => {
    btn.addEventListener('click', () => {
        document.querySelectorAll('.nav-item').forEach((b) => b.classList.remove('active'));
        btn.classList.add('active');
        document.querySelectorAll('.view').forEach((v) => v.classList.remove('active'));
        $('view-' + btn.dataset.view).classList.add('active');
        // 隐藏期间攒下的变更在这里一次性补建
        if (btn.dataset.view === 'bandwidth' && bandwidthGroupsDirty) buildBandwidthGroups();
        if (btn.dataset.view === 'tools') ensureToolsLoaded();
        if (btn.dataset.view === 'mc') $('mcAddress').focus();
    });
});

// ---------- 自定义标题栏 ----------
$('tbMin').addEventListener('click', () => WindowMinimise());
$('tbMax').addEventListener('click', () => WindowToggleMaximise());
// ---------- 关闭窗口行为：直接缩到托盘 / 弹窗询问 ----------
// 偏好以 Go 侧为准：Alt+F4、任务栏关闭和标题栏 × 都会在 Go 的 OnBeforeClose
// 里判断，只存 localStorage 覆盖不到前两者。
let closeAction = 'ask';
function syncTraySwitch() { $('setTray').checked = closeAction === 'tray'; }
async function saveCloseAction(action) {
    const next = action === 'tray' ? 'tray' : 'ask';
    await SetCloseAction(next);
    closeAction = next;
    syncTraySwitch();
}
async function initSettings() {
    let s = null;
    try { s = await GetSettings(); } catch { /* 绑定不可用时按默认值处理 */ }
    if (!s) s = { testSeconds: 10, bwParallel: 6, bwTunnelConcurrency: 2, closeAction: '' };
    if (!s.closeAction) {
        // 从 A 组版本的 localStorage 迁移一次；迁移后以 Go settings.json 为准。
        s.closeAction = readStorage('closeAction') === 'tray' ? 'tray' : 'ask';
        try {
            await SaveSettings({
                testSeconds: clampInt(s.testSeconds, 5, 30, 10),
                bwParallel: s.bwParallel,
                bwTunnelConcurrency: clampInt(s.bwTunnelConcurrency, 1, Number.MAX_SAFE_INTEGER, 2),
                closeAction: s.closeAction,
            });
        } catch { /* 保存失败也不阻塞启动 */ }
        removeStorage('closeAction');
    }
    settings.testSeconds = clampInt(s.testSeconds, 5, 30, 10);
    settings.bwParallel = clampInt(s.bwParallel, 1, 8, 6);
    settings.bwTunnelConcurrency = clampInt(s.bwTunnelConcurrency, 1, Number.MAX_SAFE_INTEGER, 2);
    syncTunnelConcurrency();
    closeAction = s.closeAction === 'tray' ? 'tray' : 'ask';
    if ($('setTestSeconds')) $('setTestSeconds').value = String(settings.testSeconds);
    if ($('setBWParallel')) $('setBWParallel').value = String(settings.bwParallel);
    syncTraySwitch();
}
async function saveTestSettings() {
    const secs = clampInt($('setTestSeconds').value, 5, 30, settings.testSeconds);
    const parallel = clampInt($('setBWParallel').value, 1, 8, settings.bwParallel);
    const concurrency = clampInt($('setBWTunnelConcurrency').value, 1, Number.MAX_SAFE_INTEGER, settings.bwTunnelConcurrency);
    try {
        const saved = await SaveSettings({ testSeconds: secs, bwParallel: parallel, bwTunnelConcurrency: concurrency, closeAction });
        settings.testSeconds = clampInt(saved.testSeconds, 5, 30, 10);
        settings.bwParallel = clampInt(saved.bwParallel, 1, 8, 6);
        settings.bwTunnelConcurrency = clampInt(saved.bwTunnelConcurrency, 1, Number.MAX_SAFE_INTEGER, concurrency);
        syncTunnelConcurrency();
        $('setTestSeconds').value = String(settings.testSeconds);
        $('setBWParallel').value = String(settings.bwParallel);
        appendLog('prefer', 'ok', `测速参数已保存：单节点 ${settings.testSeconds} 秒 · 单方向 ${settings.bwParallel} 条连接`);
    } catch (err) {
        syncTunnelConcurrency();
        $('setTestSeconds').value = String(settings.testSeconds);
        $('setBWParallel').value = String(settings.bwParallel);
        appendLog('prefer', 'err', '保存测速参数失败：' + err);
    }
}
$('tbClose').addEventListener('click', () => { RequestClose(); });
onAppEvent('closeRequested', () => {
    // Go 侧判断为“询问”时才会发这个事件；窗口已被拦住，不会退出。
    $('closeAskModal').classList.remove('hidden');
});
$('closeAskTray').addEventListener('click', async () => {
    try { await saveCloseAction('tray'); } catch { return; }
    $('closeAskModal').classList.add('hidden');
    HideToTray();
});
$('closeAskExit').addEventListener('click', () => {
    $('closeAskModal').classList.add('hidden');
    disposeAppEvents();
    QuitApp();
});
$('closeAskBackdrop').addEventListener('click', () => $('closeAskModal').classList.add('hidden'));
$('closeAskDismiss').addEventListener('click', () => $('closeAskModal').classList.add('hidden'));
$('setTray').addEventListener('change', async (e) => {
    try {
        await saveCloseAction(e.target.checked ? 'tray' : 'ask');
    } catch (err) {
        e.target.checked = closeAction === 'tray';
        appendLog('prefer', 'err', '保存关闭行为失败：' + err);
    }
});
$('setTestSeconds').addEventListener('change', saveTestSettings);
$('setBWParallel').addEventListener('change', saveTestSettings);
syncTraySwitch();

const tunnelConcurrencyRow = document.createElement('div');
tunnelConcurrencyRow.className = 'setting-row';
tunnelConcurrencyRow.innerHTML = `<div class="setting-text"><b>并行测速隧道数</b><small id="bwTunnelConcurrencyHint">自行填写同时测速的隧道数量，最少 1 条，默认 2 条；不预先检查配额，服务端拒绝创建时会停止测速。</small></div><div class="setting-value"><input id="setBWTunnelConcurrency" aria-label="并行测速隧道数" aria-describedby="bwTunnelConcurrencyHint" class="setting-input" type="number" min="1" step="1" value="2"/><span>条</span></div>`;
$('setBWParallel').closest('.settings-card').append(tunnelConcurrencyRow);
$('setBWTunnelConcurrency').addEventListener('change', saveTestSettings);
$('setTestSeconds').max = '30';
$('setTestSeconds').closest('.setting-row').querySelector('small').textContent = '带宽测速每个节点跑多久，上下行各占一半；范围 5~30 秒';
document.querySelector('#speedTaskModal .batch-row b').innerHTML = '<span id="taskTunnelConcurrency">2（期望）</span>';
document.querySelector('#speedTaskModal .batch-note li').textContent = '将按填写的并发数测速；隧道是否可创建以服务端返回为准，明确拒绝时停止并清理任务。';

function syncTunnelConcurrency() {
    $('setBWTunnelConcurrency').value = String(settings.bwTunnelConcurrency);
}

const frpcSettings = document.createElement('div');
frpcSettings.className = 'settings-group';
frpcSettings.innerHTML = `<div class="settings-group-head">frpc 测速组件</div>
    <div class="settings-card">
        <div class="setting-row frpc-cache-row">
            <div class="setting-text"><b>下载缓存管理</b><small>仅管理本应用下载的 frpc 缓存，包含未完成的下载；程序目录和 resources 中自行放置的 frpc 不受影响，测速时仍优先使用。</small></div>
            <div class="frpc-cache-actions"><button id="deleteFrpcBtn" class="btn secondary" type="button">删除 frpc</button><button id="redownloadFrpcBtn" class="btn secondary" type="button">重新下载 frpc</button></div>
        </div>
        <div class="frpc-cache-feedback"><p id="frpcCacheStatus" role="status" aria-live="polite">带宽测速期间无法管理组件。</p><progress id="frpcCacheProgress" class="hidden" max="1" value="0" aria-label="frpc 下载进度"></progress></div>
    </div>`;
document.querySelector('#view-settings .settings-list').append(frpcSettings);
let frpcCacheBusy = false;
onAppEvent('frpcCacheProgress', (ev) => {
    if (!frpcCacheBusy) return;
    $('frpcCacheStatus').textContent = ev.message || '正在下载 frpc…';
    $('frpcCacheProgress').value = ev.progress || 0;
    if (!$('frpcDownloadModal').classList.contains('hidden')) {
        $('frpcDownloadStatus').textContent = ev.message || '正在下载 frpc…';
        $('frpcDownloadProgress').value = Math.min(1, Math.max(0, ev.progress || 0));
    }
});
async function manageFrpcCache(redownload, prepare = false) {
    if (frpcCacheBusy) return;
    if (speedRunning) {
        $('frpcCacheStatus').textContent = '请先停止带宽测速，再管理 frpc。';
        return;
    }
    frpcCacheBusy = true;
    $('deleteFrpcBtn').disabled = true;
    $('redownloadFrpcBtn').disabled = true;
    $('frpcCacheStatus').textContent = redownload ? '准备重新下载 frpc…' : '正在删除 frpc 下载缓存…';
    $('frpcCacheProgress').value = 0;
    $('frpcCacheProgress').classList.toggle('hidden', !redownload);
    updateBandwidthRunUI();
    try {
        const message = prepare
            ? (await PrepareFrpc(), 'frpc 下载完成，可以开始测速。')
            : await ManageFrpcCache(redownload);
        $('frpcCacheStatus').textContent = message;
        appendSpeedLog('ok', message);
        return true;
    } catch (err) {
        $('frpcCacheStatus').textContent = '操作失败：' + err;
        if (prepare) $('frpcDownloadStatus').textContent = '下载失败：' + err;
        appendSpeedLog('err', 'frpc 缓存管理失败：' + err);
        return false;
    } finally {
        frpcCacheBusy = false;
        $('deleteFrpcBtn').disabled = false;
        $('redownloadFrpcBtn').disabled = false;
        $('frpcCacheProgress').classList.add('hidden');
        updateBandwidthRunUI();
    }
}
$('deleteFrpcBtn').addEventListener('click', () => manageFrpcCache(false));
$('redownloadFrpcBtn').addEventListener('click', () => manageFrpcCache(true));

function closeFrpcDownload() {
    if (!frpcCacheBusy) $('frpcDownloadModal').classList.add('hidden');
}
['closeFrpcDownload', 'cancelFrpcDownload', 'frpcDownloadBackdrop'].forEach((id) => {
    $(id).addEventListener('click', closeFrpcDownload);
});
$('confirmFrpcDownload').addEventListener('click', async () => {
    if (frpcCacheBusy || speedRunning || speedStarting) return;
    const controls = ['confirmFrpcDownload', 'cancelFrpcDownload', 'closeFrpcDownload'];
    controls.forEach((id) => { $(id).disabled = true; });
    $('confirmFrpcDownload').textContent = '下载中…';
    $('frpcDownloadStatus').textContent = '正在准备下载，尚未开始测速…';
    $('frpcDownloadProgress').value = 0;
    $('frpcDownloadProgress').classList.remove('hidden');
    try {
        if (await manageFrpcCache(true, true)) {
            $('frpcDownloadModal').classList.add('hidden');
            showSpeedTask();
        }
    } finally {
        controls.forEach((id) => { $(id).disabled = false; });
        $('confirmFrpcDownload').textContent = '下载 frpc';
        $('frpcDownloadProgress').classList.add('hidden');
    }
});

let qzhuaLoggedIn = false;
// 节点请求版本号：退出登录、重新登录、连续刷新都会递增，
// 在途的旧请求返回后据此丢弃，避免旧账号的数据写回界面。
let nodeLoadVersion = 0;
let loginVerifyUrl = '';

const setText = (id, value, fallback = '—') => {
    const el = $(id);
    if (el) el.textContent = value === undefined || value === null || value === '' ? fallback : String(value);
};
const setAvatar = (img, source, fallbackText) => {
    if (!img) return;
    img.onerror = () => {
        img.removeAttribute('src');
        img.alt = fallbackText || '用户头像';
        img.classList.add('avatar-fallback');
    };
    img.classList.remove('avatar-fallback');
    if (source) img.src = source;
    else {
        img.removeAttribute('src');
        img.alt = fallbackText || '用户头像';
        img.classList.add('avatar-fallback');
    }
};
const renderQZhuaProfile = (profile) => {
    const qzhua = profile?.qzhua || {};
    const chml = profile?.chml || null;
    // 设备码流程只申请 chmlfrp_api scope，OIDC userinfo 可能取不到昵称和头像；
    // 登录成功后的 CHMLFrp /userinfo 提供 username、userimg，优先用于应用账户卡片。
    const name = chml?.username || qzhua.username || qzhua.preferredUsername || qzhua.nickname || qzhua.name || qzhua.email || '轻爪用户';
    const picture = chml?.userimg || qzhua.picture || qzhua.avatar || '';
    setText('accTitle', name);
    setText('accSubtitle', chml?.usergroup || qzhua.email || '已登录');
    $('sideAccount').classList.add('logged-in');
    setAvatar($('accAvatar'), picture, name.slice(0, 1));
};

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

onAppEvent('qzhuaAuth', (event) => {
    if (event.status === 'logged_in') {
        qzhuaLoggedIn = true;
        renderQZhuaProfile(event.profile || { qzhua: {
            username: event.username,
            email: event.email,
            picture: event.picture,
        } });
        $('loginModal').classList.add('hidden');
        // 未登录时带宽页会把「当前测速节点」写成「请先登录账号」，登录成功后必须复位，
        // 否则这条提示会一直留在进度条上方，直到用户真正开始一次测速。
        if (!speedRunning && $('currentSpeedNode')) $('currentSpeedNode').textContent = '未开始';
        // 登录成功：撤掉节点区的登录引导，并真正去拉节点（未登录期间不展示卡片）
        ensureLoginHint($('groups'));
        ensureLoginHint($('bandwidthGroups'));
        loadNodes();
        return;
    }
    qzhuaLoggedIn = false;
    $('sideAccount').classList.remove('logged-in');
    $('accAvatar').removeAttribute('src');
    $('accTitle').textContent = '未登录';
    if (event.status === 'waiting') {
        if (event.verifyUrl) loginVerifyUrl = event.verifyUrl;
        if (event.userCode) $('loginUserCode').textContent = event.userCode;
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
    // 退出登录/登录态失效：节点卡片一并收掉，回到登录引导
    renderLoginOnly();
});

$('accountLoginBtn').addEventListener('click', () => {
    if (!qzhuaLoggedIn) openLoginModal();
});

$('loginAuthBtn').addEventListener('click', async () => {
    const btn = $('loginAuthBtn');
    btn.disabled = true;
    $('loginError').classList.add('hidden');
    try {
        const res = await StartQZhuaLogin();
        loginVerifyUrl = res.verifyUrl || '';
        $('loginUserCode').textContent = res.userCode || '------';
        $('loginStep1').classList.add('hidden');
        $('loginStep2').classList.remove('hidden');
    } catch (err) {
        $('loginError').textContent = String(err);
        $('loginError').classList.remove('hidden');
    } finally {
        btn.disabled = false;
    }
});

$('loginCopyCode').addEventListener('click', async () => {
    const btn = $('loginCopyCode');
    const ok = await copyText($('loginUserCode').textContent.trim());
    btn.textContent = ok ? '已复制' : '复制失败';
    setTimeout(() => { btn.textContent = '复制配对码'; }, 1500);
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
$('accountLogoutBtn').addEventListener('click', async () => {
    const btn = $('accountLogoutBtn');
    if (!qzhuaLoggedIn || btn.disabled) return;
    btn.disabled = true;
    try {
        await LogoutQZhua();
        $('accountLoginBtn').focus();
    } catch (err) {
        $('accSubtitle').textContent = '退出失败，请重试';
        appendLog('prefer', 'err', '退出登录失败：' + err);
    } finally {
        btn.disabled = false;
    }
});

$('loginRegister').addEventListener('click', (e) => {
    e.preventDefault();
    BrowserOpenURL('https://account-api.qzhua.net/register');
});

// “保持登录状态”现在是真的：勾选才把令牌加密落盘，取消勾选会顺手删掉已存的凭据
$('loginKeep').addEventListener('change', async (e) => {
    try {
        await SetRememberLogin(Boolean(e.target.checked));
        appendLog('prefer', 'info', e.target.checked ? '已开启保持登录' : '已关闭保持登录，本地凭据已清除');
    } catch (err) {
        $('loginKeep').checked = !e.target.checked;
        appendLog('prefer', 'err', '切换保持登录失败：' + err);
    }
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
const logLevelTag = (level) => (level === 'ok' ? '成功' : level === 'err' ? '报错' : level === 'warn' ? '警告' : '信息');

const updateLogCounts = () => {
    const ok = runLogs.filter((x) => x.level === 'ok').length;
    const err = runLogs.filter((x) => x.level === 'err').length;
    const warn = runLogs.filter((x) => x.level === 'warn').length;
    if ($('logTotalCount')) $('logTotalCount').textContent = String(runLogs.length);
    if ($('logOkCount')) $('logOkCount').textContent = String(ok);
    if ($('logErrCount')) $('logErrCount').textContent = String(err);
    if ($('logWarnCount')) $('logWarnCount').textContent = String(warn);
};

// 当前筛选条件下的日志：渲染与导出共用同一份口径，避免两边不一致
const filteredLogRows = () => runLogs.filter((x) =>
    (logFilterSource === 'all' || x.source === logFilterSource)
    && (!logQuery || x.msg.toLowerCase().includes(logQuery))
);

const createLogRow = (x) => {
    const el = document.createElement('div');
    el.className = 'log-line';
    el.innerHTML = '<span class="log-time"></span><span class="log-source"></span><span class="log-tag"></span><span class="log-msg"></span>';
    // 全部用 textContent 写入，避免节点名等文本注入 HTML
    el.querySelector('.log-time').textContent = x.time;
    el.querySelector('.log-source').textContent = `[${logSourceLabel(x.source)}]`;
    el.querySelector('.log-tag').textContent = `[${logLevelTag(x.level)}]`;
    el.querySelector('.log-msg').textContent = x.msg;
    return el;
};

// 上一次渲染的锚点（最后一条日志对象 + 行数 + 筛选签名）。
// 只要锚点还在当前结果里，就说明这次只是尾部追加，补几行即可；
// 每帧全量重建最多 1000 行 DOM 是测速时掉帧的主要来源之一。
let logRenderAnchor = null;
const logFilterSig = () => `${logFilterSource}|${logQuery}`;

const renderLogs = () => {
    const list = $('logList');
    if (!list) return;
    const rows = filteredLogRows();
    if ($('logFilterCount')) $('logFilterCount').textContent = `${rows.length} 条`;
    if (!rows.length) {
        list.innerHTML = `<div class="log-empty"><b>暂无日志</b><small>${runLogs.length ? '当前筛选条件下没有匹配记录' : '执行延迟测速或带宽测速后，记录会显示在这里'}</small></div>`;
        logRenderAnchor = null;
        return;
    }
    const sig = logFilterSig();
    const anchor = logRenderAnchor;
    // indexOf 是对象身份比较：只用同一个数组里的引用才认
    const from = anchor && anchor.sig === sig ? rows.indexOf(anchor.tail) : -1;
    if (from >= 0) {
        // 超过 1000 条上限后 runLogs 从头部丢日志：DOM 头部对应那几行也要删掉，
        // 否则界面会一直挂着已经不在内存里的旧日志。
        const stale = anchor.len - 1 - from;
        for (let i = 0; i < stale; i++) list.firstElementChild?.remove();
        if (rows.length > from + 1) {
            const frag = document.createDocumentFragment();
            for (let i = from + 1; i < rows.length; i++) frag.appendChild(createLogRow(rows[i]));
            list.appendChild(frag);
        }
    } else {
        const frag = document.createDocumentFragment();
        rows.forEach((x) => frag.appendChild(createLogRow(x)));
        list.innerHTML = '';
        list.appendChild(frag);
    }
    logRenderAnchor = { sig, len: rows.length, tail: rows[rows.length - 1] };
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
    // 同步落盘：内存日志重启即丢，用户反馈问题时基本没法排查
    try {
        AppendRunLog?.(source, level, msg)?.catch?.(() => {});
    } catch {
        /* 忽略：日志写盘失败不该影响测速 */
    }
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
$('logSearch')?.addEventListener('input', debounce((e) => {
    logQuery = e.target.value.trim().toLowerCase();
    renderLogs();
}));
$('logClearBtn')?.addEventListener('click', () => {
    runLogs.length = 0;
    updateLogCounts();
    renderLogs();
});
// 导出当前筛选结果：带上来源/条数/落盘目录，方便用户直接把文件发给作者
$('logExportBtn')?.addEventListener('click', async () => {
    const btn = $('logExportBtn');
    const restore = () => { btn.innerHTML = '<span>↓</span>导出'; };
    const rows = filteredLogRows();
    if (!rows.length) {
        btn.textContent = '无日志可导出';
        setTimeout(restore, 1400);
        return;
    }
    let dir = '';
    try { dir = await GetRunLogPath(); } catch { dir = ''; }
    const scope = logFilterSource === 'all' ? '全部' : logSourceLabel(logFilterSource);
    const head = [
        'CHMLFrp 测速 · 运行日志',
        `导出时间：${new Date().toLocaleString('zh-CN')}`,
        `筛选来源：${scope}${logQuery ? `，关键词「${logQuery}」` : ''}`,
        `条数：${rows.length}`,
        dir ? `落盘目录：${dir}` : '',
    ].filter(Boolean).join('\r\n');
    const body = rows
        .map((x) => `${x.time} [${logSourceLabel(x.source)}][${logLevelTag(x.level)}] ${x.msg}`)
        .join('\r\n');
    try {
        const path = await ExportRunLog(`${head}\r\n\r\n${body}\r\n`);
        if (path) {
            btn.textContent = '已导出';
            setTimeout(restore, 1400);
        }
    } catch (err) {
        btn.textContent = '导出失败';
        setTimeout(restore, 1600);
        appendLog('prefer', 'err', '导出日志失败：' + err);
    }
});
let speedPaused = false;
let speedToggling = false; // 暂停/继续请求在途，挡住连点发出互相矛盾的指令
let speedMeasuring = false;
let speedSamples = [];
const nodeSpeedState = new Map();
let detailNodeId = null;
const activeSpeedNodes = new Set();
const completedSpeedNodes = new Set();

function followActiveSpeedNode() {
    if ($('bandwidthDetail').classList.contains('hidden')) return;
    if (detailNodeId !== null && !completedSpeedNodes.has(detailNodeId)) return;
    const next = activeSpeedNodes.values().next().value;
    if (next !== undefined) showBandwidthDetail(nodes.find((node) => String(node.id) === next), false);
}
let bandwidthRunning = false;
let speedStarting = false; // 已点击“开始测试”、请求尚未返回，挡住连点
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
        tip.innerHTML = `<div class="ct-title">第 ${item.t} 秒</div><div class="ct-row"><i class="ct-dot down"></i>下载 ${item.phase === 'UP' ? '—' : `${formatSpeed(item.down)} Mbps`}</div><div class="ct-row"><i class="ct-dot up"></i>上传 ${item.phase === 'DN' ? '—' : `${formatSpeed(item.up)} Mbps`}</div>`;
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
        // 曲线颜色取自 CSS 变量：暗色主题是浅色描边，亮色主题换成深色，
        // 否则白底上这条 #e8eaed 的线基本看不见。
        const cssVars = getComputedStyle(document.documentElement);
        const chartColor = (name, fallback) => (cssVars.getPropertyValue(name).trim() || fallback);
        const downColor = chartColor('--chart-down', '#e8eaed');
        const upColor = chartColor('--chart-up', '#8b939e');
        const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
        svg.setAttribute('class', 'speed-curve');
        svg.setAttribute('viewBox', '0 0 100 100');
        svg.setAttribute('preserveAspectRatio', 'none');
        svg.innerHTML = `<defs><linearGradient id="${gid}Down" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${downColor}" stop-opacity=".20"/><stop offset="1" stop-color="${downColor}" stop-opacity="0"/></linearGradient><linearGradient id="${gid}Up" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${upColor}" stop-opacity=".16"/><stop offset="1" stop-color="${upColor}" stop-opacity="0"/></linearGradient></defs><path d="${downPath} L 100 100 L 0 100 Z" fill="url(#${gid}Down)"/><path d="${upPath} L 100 100 L 0 100 Z" fill="url(#${gid}Up)"/><path d="${upPath}" fill="none" stroke="${upColor}" stroke-width="1.4" vector-effect="non-scaling-stroke" stroke-linecap="round" stroke-linejoin="round"/><path d="${downPath}" fill="none" stroke="${downColor}" stroke-width="1.6" vector-effect="non-scaling-stroke" stroke-linecap="round" stroke-linejoin="round"/>`;
        svg.chartData = {
            list,
            x: downPts.map((p) => p[0]),
            downY: downPts.map((p) => p[1]),
            upY: upPts.map((p) => p[1]),
        };
        chart.appendChild(svg);
    });
};
// 带宽测速按钮的门槛：没加载节点、没选节点都不允许开始。
// 运行中保留“暂停/继续”（长任务有意义），所以只在空闲态禁用。
function updateBandwidthRunUI() {
    const btn = $('speedBtn');
    if (!btn) return;
    if (speedRunning) {
        btn.disabled = false;
        setTestButtonState(btn, speedPaused ? '继续' : '暂停', !speedPaused && !speedMeasuring, !speedPaused && speedMeasuring);
        btn.title = speedPaused ? '继续打流' : '暂停打流（总时长会顺延）';
        btn.classList.toggle('speed-resume', speedPaused);
        return;
    }
    btn.classList.remove('speed-resume');
    const ready = nodes.length > 0 && selected.size > 0;
    btn.disabled = !ready || frpcCacheBusy || speedStarting;
    setTestButtonState(btn, '开始测速', false);
    btn.title = frpcCacheBusy ? '请等待 frpc 管理完成' : (!nodes.length ? '请先刷新加载节点' : (selected.size === 0 ? '请先选择至少一个节点' : ''));
}

const setSpeedRunning = (running) => {
    speedRunning = running;
    bandwidthRunning = running;
    if (!running) {
        speedPaused = false;
        speedMeasuring = false;
    }
    $('speedBtn').classList.remove('hidden');
    $('speedCancelBtn').classList.toggle('hidden', !running);
    updateBandwidthRunUI();
    updateExportUI();
    syncRefreshButtons();
};
onAppEvent('speed', (ev) => {
    if (!speedMeasuring && speedRunning) {
        speedMeasuring = true;
        updateBandwidthRunUI();
    }
    const key = String(ev.nodeId);
    activeSpeedNodes.add(key);
    let state = nodeSpeedState.get(key);
    if (!state) {
        state = { samples: [], lastSecond: -1, down: null, up: null };
        nodeSpeedState.set(key, state);
    }
    const sampleSecond = Math.floor(ev.elapsed);
    const shouldDrawSample = sampleSecond > state.lastSecond;
    if (shouldDrawSample) {
        state.samples.push({ up: ev.up, down: ev.down, t: sampleSecond, phase: ev.phase });
        if (state.samples.length > 60) state.samples.shift();
        state.lastSecond = sampleSecond;
    }
    state.tick = ev;
    if (ev.phase !== 'UP') state.down = ev.down;
    if (ev.phase !== 'DN') state.up = ev.up;
    followActiveSpeedNode();
    if (key !== detailNodeId) return;
    speedSamples = state.samples;
    // 分时测量：当前阶段只测一个方向，另一方向没有数据，别把面板数字刷成 0
    if (ev.phase !== 'UP' && $('speedDown')) $('speedDown').innerHTML = `${formatSpeed(ev.down)}<em> Mbps</em>`;
    if (ev.phase !== 'DN' && $('speedUp')) $('speedUp').innerHTML = `${formatSpeed(ev.up)}<em> Mbps</em>`;
    if (ev.phase !== 'UP' && $('detailDown')) $('detailDown').textContent = `${formatSpeed(ev.down)} Mbps`;
    if (ev.phase !== 'DN' && $('detailUp')) $('detailUp').textContent = `${formatSpeed(ev.up)} Mbps`;
    if ($('speedElapsed')) {
        // 分时测量：先下行再上行，把当前方向标出来，用户才知道曲线为什么一半是 0
        const dir = ev.phase === 'DN' ? '下行 ' : (ev.phase === 'UP' ? '上行 ' : '');
        $('speedElapsed').textContent = `${dir}${ev.elapsed.toFixed(1)} / ${ev.total.toFixed(0)} 秒`;
    }
    if (shouldDrawSample) drawSpeedChart();
});
onAppEvent('speedRunProgress', (ev) => {
    const progress = Math.round(Math.min(1, Math.max(0, ev.progress || 0)) * 100);
    if ($('speedProgress')) $('speedProgress').innerHTML = `${progress}<em>%</em>`;
    $('bandwidthProgressFill').style.width = `${progress}%`;
    $('currentSpeedNode').textContent = `运行中 ${ev.active} 个任务 · 已完成 ${ev.completed}/${ev.total}`;
});
onAppEvent('speedLog', (ev) => {
    appendSpeedLog(ev.level || 'info', ev.msg || '');
});
// 单节点完成：把该节点结果写进卡片与排行榜（多节点串行时逐个落地）
onAppEvent('speedNodeDone', (ev) => {
    const key = String(ev.id);
    activeSpeedNodes.delete(key);
    completedSpeedNodes.add(key);
    setRing(key, 100);
    if (ev.error) {
        bandwidthResults.set(key, { ...(bandwidthResults.get(key) || {}), error: ev.error });
        if (key === detailNodeId) showBandwidthDetail(nodes.find((node) => String(node.id) === key), false);
        updateBandwidthCard(key);
        followActiveSpeedNode();
        return;
    }
    bandwidthResults.set(key, {
        ...(bandwidthResults.get(key) || {}),
        delay: ev.delay || bandwidthResults.get(key)?.delay,
        down: formatSpeed(ev.downAvg),
        up: formatSpeed(ev.upAvg),
        average: formatSpeed((ev.downAvg + ev.upAvg) / 2),
        duration: String(ev.seconds || settings.testSeconds),
        error: '',
    });
    if (key !== '__last__') bandwidthResults.delete('__last__');
    if (key === detailNodeId) showBandwidthDetail(nodes.find((node) => String(node.id) === key), false);
    // 只更新这一张卡 + 按帧合并的排行榜：逐个全量重建在 N 个节点上是 O(N²)
    updateBandwidthCard(key);
    scheduleBandwidthLeaderboard();
    $('bandwidthLeaderboard')?.classList.remove('hidden');
    followActiveSpeedNode();
});
onAppEvent('speedDone', (ev) => {
    setSpeedRunning(false);
    if ($('speedDown')) $('speedDown').innerHTML = `${formatSpeed(ev.downAvg)}<em> Mbps</em>`;
    if ($('speedUp')) $('speedUp').innerHTML = `${formatSpeed(ev.upAvg)}<em> Mbps</em>`;
    if ($('speedProgress')) $('speedProgress').innerHTML = `${ev.cancelled ? '已停止' : '完成'}`;
    $('bandwidthProgressFill').style.width = ev.cancelled ? '0%' : '100%';
    $('currentSpeedNode').textContent = ev.cancelled ? '已停止' : '已完成';
    // 单节点结果由 speedNodeDone 落地，这里只负责整轮收尾
    if (ev.cancelled) {
        // 先撤掉排队中的排行榜渲染，否则下一帧会把刚隐藏的榜单又画出来
        cancelScheduledBandwidthLeaderboard();
        $('bandwidthLeaderboard')?.classList.add('hidden');
        resetRings();
    } else {
        renderBandwidthLeaderboard();
    }
    if (ev.cancelled) {
        appendSpeedLog('warn', '测速已停止');
    } else {
        appendSpeedLog('ok', `测速完成 · 下载 ${formatSpeed(ev.downAvg)} Mbps · 上传 ${formatSpeed(ev.upAvg)} Mbps · 峰值 ${formatSpeed(ev.downPeak)}/${formatSpeed(ev.upPeak)}`);
    }
    updateExportUI();
});
// 后端判定未登录 / 登录已过期（带宽测速要建临时隧道，没有令牌跑不了）
onAppEvent('speedNeedLogin', () => {
    setSpeedRunning(false);
    if ($('currentSpeedNode')) $('currentSpeedNode').textContent = '请先登录账号';
    appendSpeedLog('warn', qzhuaLoggedIn ? '登录已过期，请重新登录 CHMLFrp' : '未登录：带宽测速需要在账号里创建临时隧道');
    renderLoginOnly();
    openLoginModal();
});
const startSpeedRun = async () => {
    activeSpeedNodes.clear();
    completedSpeedNodes.clear();
    for (const id of selected) {
        nodeSpeedState.delete(String(id));
        bandwidthResults.delete(String(id));
    }
    speedSamples = [];
    drawSpeedChart();
    resetRings();
    $('bandwidthLeaderboard')?.classList.add('hidden');
    if ($('speedDown')) $('speedDown').innerHTML = '--<em> Mbps</em>';
    if ($('speedUp')) $('speedUp').innerHTML = '--<em> Mbps</em>';
    if (detailNodeId !== null) showBandwidthDetail(nodes.find((node) => String(node.id) === detailNodeId), false);
    if ($('speedProgress')) $('speedProgress').innerHTML = '0<em>%</em>';
    $('bandwidthProgressFill').style.width = '0%';
    const currentNode = nodes.find((node) => selected.has(String(node.id)));
    $('currentSpeedNode').textContent = currentNode?.name || '当前测速任务';
    const targetLabel = selected.size === 1 && currentNode ? currentNode.name : `已选 ${selected.size} 个节点`;
    appendSpeedLog('info', `开始测速：${targetLabel} · 单节点 ${settings.testSeconds}s · 单方向 ${settings.bwParallel} 条连接 · 每个节点会临时建隧道并启动 frpc`);
    closeExportMenu('bandwidth');
    setSpeedRunning(true);
    speedStarting = true;
    try {
        await StartSpeedTest(settings.testSeconds, settings.bwParallel, [...selected].map(Number));
    } catch (err) {
        setSpeedRunning(false);
        appendSpeedLog('err', '启动带宽测速失败：' + err);
    } finally {
        speedStarting = false;
    }
};
$('speedBtn').addEventListener('click', async () => {
    if (frpcCacheBusy || speedStarting) return;
    if (speedRunning) {
        if (speedToggling) return;
        // 以后端返回结果为准再改前端状态：先翻转本地变量、请求失败就会和真实状态脱节。
        const nextPaused = !speedPaused;
        speedToggling = true;
        try {
            if (nextPaused) await PauseSpeedTest();
            else await ResumeSpeedTest();
            // 请求期间测速可能已经自然结束，这时再改暂停态会让下一次启动显示成「继续」
            if (speedRunning) {
                speedPaused = nextPaused;
                appendSpeedLog('info', speedPaused ? '测速已暂停' : '测速已恢复');
            }
        } catch (err) {
            appendSpeedLog('err', `切换测速状态失败：${err}`);
        } finally {
            speedToggling = false;
            // 只刷新按钮文案：不能无条件 setSpeedRunning(true)，
            // 否则测速恰好在这段时间里结束时，会把界面重新标成「运行中」。
            if (speedRunning) updateBandwidthRunUI();
        }
        return;
    }
    // 带宽测速必须登录（要建临时隧道），未登录时直接引导，不弹任务框
    if (!qzhuaLoggedIn) {
        if ($('currentSpeedNode')) $('currentSpeedNode').textContent = '请先登录账号';
        appendSpeedLog('warn', '未登录：带宽测速需要登录账号后在服务端创建临时隧道');
        renderLoginOnly();
        openLoginModal();
        return;
    }
    // 带宽测速会真实占用隧道配额和流量，必须让用户明确选节点，不能默认全测。
    if (!nodes.length) {
        appendSpeedLog('warn', '请先刷新加载节点，再选择要测速的节点');
        updateBandwidthRunUI();
        return;
    }
    if (selected.size === 0) {
        appendSpeedLog('warn', '请先选择至少一个节点');
        updateBandwidthRunUI();
        return;
    }
    if (await checkFrpcBeforeSpeed()) showSpeedTask();
});

async function checkFrpcBeforeSpeed() {
    if (speedStarting || speedRunning || frpcCacheBusy) return false;
    speedStarting = true;
    updateBandwidthRunUI();
    try {
        if (await IsFrpcReady()) return true;
        $('speedTaskModal').classList.add('hidden');
        $('frpcDownloadStatus').textContent = '下载期间不会开始测速。';
        $('frpcDownloadProgress').value = 0;
        $('frpcDownloadProgress').classList.add('hidden');
        $('frpcDownloadModal').classList.remove('hidden');
        return false;
    } catch (err) {
        $('currentSpeedNode').textContent = '组件检查失败，请重试';
        appendSpeedLog('err', '检查 frpc 失败：' + err);
        return false;
    } finally {
        speedStarting = false;
        updateBandwidthRunUI();
    }
}

function showSpeedTask() {
    // 静态确认弹窗：只显示一次性的任务信息，测速过程中不刷新
    const count = selected.size;
    $('taskNodeCount').textContent = String(count);
    $('taskSecs').textContent = String(settings.testSeconds);
    $('taskParallel').textContent = String(settings.bwParallel);
    // 每节点还要建隧道 + 起 frpc（约 5~10 秒），预估时长按“单节点时长 + 10 秒”算
    $('taskTunnelConcurrency').textContent = `${settings.bwTunnelConcurrency}（期望）`;
    $('taskEta').textContent = `约 ${Math.ceil(count / settings.bwTunnelConcurrency) * (settings.testSeconds + 10)} 秒`;
    $('speedTaskModal').classList.remove('hidden');
}
$('taskStartBtn').addEventListener('click', async () => {
    // 连点会触发两轮并发测速，后端虽有兜底，前端也不该把请求发出去
    if (speedStarting || speedRunning || frpcCacheBusy) return;
    if (selected.size === 0) {
        $('speedTaskModal').classList.add('hidden');
        appendSpeedLog('warn', '请先选择至少一个节点');
        updateBandwidthRunUI();
        return;
    }
    if (await checkFrpcBeforeSpeed()) {
        $('speedTaskModal').classList.add('hidden');
        startSpeedRun();
    }
});
$('speedCancelBtn').addEventListener('click', async () => {
    const btn = $('speedCancelBtn');
    // 停止请求在途时重复点击没有意义，后端也会拒绝第二次取消
    if (btn.disabled) return;
    btn.disabled = true;
    appendSpeedLog('warn', '用户点击停止测速');
    try {
        await CancelSpeedTest();
        $('currentSpeedNode').textContent = '未开始';
    } catch (err) {
        appendSpeedLog('err', `停止测速失败：${err}`);
    } finally {
        btn.disabled = false;
    }
});

onAppEvent('speedNode', (ev) => {
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

onAppEvent('speedNodeProgress', (ev) => {
    const key = String(ev.id);
    setRing(key, ev.progress * 100);
    if (ev.progress < 1 && !completedSpeedNodes.has(key)) {
        activeSpeedNodes.add(key);
        followActiveSpeedNode();
    }
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
    // TCPing 失败但 ICMP 在线：显示 ICMP 延迟而不是"超时"。
    // ICMP 结果存在 preferStats（finish 后 buildGroups 重渲染卡片也走这里，
    // 否则测速中显示的 ICMP 会被重渲染打回"超时"）。
    const st = preferStats.get(n.id);
    const icmpOnline = d !== undefined && d < 0 && typeof st?.icmp === 'number' && st.icmp >= 0;
    const delayText = d === undefined ? '--' : (d < 0 ? (icmpOnline ? `ICMP ${Math.round(st.icmp)}ms` : '超时') : `${d}ms`);
    const delayClass = d === undefined ? '' : `delay-value ${d < 0 ? (icmpOnline ? 'medium' : 'slow') : delayTone(d)}`;
    // TCPing 统计：未测过显示 --，测过显示真实次数/丢包率
    const sentText = st ? `${st.sent} 次` : '--';
    const recvText = st ? `${st.recv} 次` : '--';
    const lossText = st ? `${Math.round(st.loss * 100)}%` : '--';
    return `
        <div class="nc-top">
            <span class="nc-id">#${n.id}</span>
            <span>${pill}</span>
        </div>
        <div class="nc-name">${esc(n.name)}</div>
        ${n.ip ? '<div class="nc-ip nc-id">IP ' + esc(n.ip) + '</div>' : ''}
        <div class="prefer-metrics"><span>已发送 <b data-stat="sent">${sentText}</b></span><span>已接收 <b data-stat="recv">${recvText}</b></span><span>丢包率 <b data-stat="loss">${lossText}</b></span><span>平均延迟 <b data-delay-value class="${delayClass}" >${delayText}</b></span></div>
        <button class="prefer-detail-btn" type="button" data-prefer-id="${n.id}">详情</button>
    `;
}

function renderLeaderboard(boardId, listId, metaId, ranking, opts = {}) {
    const board = $(boardId);
    const list = $(listId);
    if (!board || !list || !ranking?.length) return;
    const unit = opts.unit || 'ms';
    const top = ranking.slice(0, 3);
    const rest = ranking.slice(3, 5);
    const itemHTML = (item, index) => {
        const node = nodes.find((candidate) => candidate.id === item.id);
        return `
        <div class="ranking-item">
            <span class="ranking-rank">${index + 1}</span>
            <div class="ranking-name">${esc(item.name)}<span class="ranking-region">${node ? regionLabel(node.china) : '节点'} · #${item.id}</span></div>
            <span class="ranking-delay">${item.value}<small> ${unit}</small></span>
        </div>`;
    };
    list.innerHTML = top.map(itemHTML).join('') + (rest.length
        ? `<div class="ranking-rest">${rest.map((item, index) => itemHTML(item, index + 3)).join('')}</div>`
        : '');
    $(metaId).textContent = opts.meta || `共 ${ranking.length} 个节点 · 延迟越低越好`;
    board.classList.remove('hidden');
}

function renderPreferLeaderboard(ranking) {
    renderLeaderboard('preferLeaderboard', 'preferRankingList', 'preferLeaderboardMeta',
        ranking.map((item) => ({ id: item.id, name: item.name, value: item.delay })),
        { unit: 'ms', meta: `共 ${ranking.length} 个节点 · 延迟越低越好` });
}

// 带宽排行：默认按下载速度排（越高越好），也可切到上传或延迟
let bandwidthRankDim = 'down';
const rankDimMeta = {
    down: { label: '下载', unit: 'Mbps', better: '越高越好' },
    up: { label: '上传', unit: 'Mbps', better: '越高越好' },
    delay: { label: '延迟', unit: 'ms', better: '越低越好' },
};

function bandwidthRanking(dim = bandwidthRankDim) {
    const ranking = [];
    for (const [key, result] of bandwidthResults) {
        if (key === '__last__' || !result || result.error) continue;
        const node = nodes.find((candidate) => String(candidate.id) === key);
        if (!node) continue;
        const raw = dim === 'delay' ? result.delay : Number(result[dim]);
        if (raw === undefined || raw === null || raw === '') continue;
        const value = Number(raw);
        if (!Number.isFinite(value) || (dim !== 'delay' && value <= 0)) continue;
        ranking.push({ id: node.id, name: node.name, value: dim === 'delay' ? value : Math.round(value * 10) / 10 });
    }
    // 带宽越高越好，延迟越低越好
    ranking.sort((a, b) => (dim === 'delay' ? a.value - b.value : b.value - a.value));
    return ranking;
}

function renderBandwidthLeaderboard() {
    const dim = bandwidthRankDim;
    const meta = rankDimMeta[dim] || rankDimMeta.down;
    const ranking = bandwidthRanking(dim);
    if (!ranking.length) return;
    renderLeaderboard('bandwidthLeaderboard', 'bandwidthRankingList', 'bandwidthLeaderboardMeta', ranking, {
        unit: meta.unit,
        meta: `共 ${ranking.length} 个节点 · ${meta.label}${meta.better}`,
    });
}

document.querySelectorAll('.rank-dim').forEach((btn) => {
    btn.addEventListener('click', () => {
        bandwidthRankDim = btn.dataset.rankDim;
        document.querySelectorAll('.rank-dim').forEach((b) => b.classList.toggle('active', b === btn));
        renderBandwidthLeaderboard();
    });
});

function hidePreferLeaderboard() {
    $('preferLeaderboard')?.classList.add('hidden');
}

// 带宽视图的卡片是否落后于数据：隐藏时只标脏，切过去再重建
let bandwidthGroupsDirty = false;

function bandwidthCardHTML(n) {
    const pill = n.group === 'vip' ? '<span class="pill vip">VIP</span>' : '<span class="pill user">普通</span>';
    const result = bandwidthResults.get(String(n.id));
    const d = result?.delay;
    return `<div class="nc-top"><span class="nc-id">#${n.id}</span><span>${pill}</span></div><div class="nc-name">${esc(n.name)}</div><div class="bandwidth-metrics"><span>延迟 <b data-bandwidth-delay="${n.id}" class="${d === undefined ? '' : `delay-value ${delayTone(d)}`}">${d === undefined ? '--' : `${d}ms`}</b></span><span>下载 <b data-bandwidth-down="${n.id}">${result?.down ?? '--'}</b></span><span>上传 <b data-bandwidth-up="${n.id}">${result?.up ?? '--'}</b></span></div><svg class="nc-ring" viewBox="0 0 36 36" aria-hidden="true"><circle class="nc-ring-track" cx="18" cy="18" r="15.9" pathLength="100"/><circle class="nc-ring-fg" data-ring="${n.id}" cx="18" cy="18" r="15.9" pathLength="100" stroke-dasharray="100" stroke-dashoffset="100"/></svg><button class="node-detail-btn" type="button" data-test-id="${n.id}">详情</button>`;
}

// 单节点结果落地时只改这一张卡。多节点串行跑，每完成一个就全量重建
// 整个卡片列表是 O(N²) 的 DOM 重排，节点多时会明显卡。
function updateBandwidthCard(id) {
    if (bandwidthGroupsDirty) return; // 整块待重建，这里改了也会被覆盖
    const key = String(id);
    const result = bandwidthResults.get(key);
    if (!result) return;
    const delayEl = document.querySelector(`[data-bandwidth-delay="${key}"]`);
    if (delayEl) {
        const d = result.delay;
        delayEl.textContent = d === undefined ? '--' : `${d}ms`;
        delayEl.className = d === undefined ? '' : `delay-value ${delayTone(d)}`;
    }
    const downEl = document.querySelector(`[data-bandwidth-down="${key}"]`);
    if (downEl) downEl.textContent = result.down ?? '--';
    const upEl = document.querySelector(`[data-bandwidth-up="${key}"]`);
    if (upEl) upEl.textContent = result.up ?? '--';
}

// 排行榜按帧合并：N 个节点各自触发一次重排，逐次渲染同样是 O(N²)。
let bandwidthBoardRaf = 0;
function scheduleBandwidthLeaderboard() {
    if (bandwidthBoardRaf) return;
    bandwidthBoardRaf = requestAnimationFrame(() => {
        bandwidthBoardRaf = 0;
        renderBandwidthLeaderboard();
    });
}

// 取消排队中的一次重排：测速被中止时队列里那一帧会把刚隐藏的排行榜又显示出来
function cancelScheduledBandwidthLeaderboard() {
    if (!bandwidthBoardRaf) return;
    cancelAnimationFrame(bandwidthBoardRaf);
    bandwidthBoardRaf = 0;
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
    ensureLoginHint(wrap);
    updateSelUI();
    syncBandwidthCards();
    updateExportUI();
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
    ensureLoginHint(wrap);
    updateBandwidthRunUI();
    updateExportUI();
    bandwidthGroupsDirty = false;
}

// 两个视图（延迟 / 带宽）各自一份卡片，同一时刻只有一个可见。
// 任一视图的筛选、搜索、选中变化都只需要重建自己：把另一份标脏，
// 等真正切过去再建，避免一次按键把两套 DOM 都重排一遍。
function isViewActive(id) {
    return Boolean($(id)?.classList.contains('active'));
}

function syncBandwidthCards() {
    if ($('bandwidthSelectAllBtn')) $('bandwidthSelectAllBtn').textContent = isAllSelected() ? '取消全选' : '全选';
    if (isViewActive('view-bandwidth')) {
        buildBandwidthGroups();
        return;
    }
    bandwidthGroupsDirty = true;
}

function showBandwidthDetail(node, open = true) {
    if (!node) return;
    detailNodeId = String(node.id);
    const result = bandwidthResults.get(detailNodeId);
    const state = nodeSpeedState.get(detailNodeId);
    speedSamples = state?.samples || [];
    if (open) $('bandwidthDetail').classList.remove('hidden');
    $('detailNodeName').textContent = node.name;
    $('detailDown').textContent = result?.down ? `${result.down} Mbps` : '--';
    $('detailUp').textContent = result?.up ? `${result.up} Mbps` : '--';
    $('detailAverage').textContent = result?.average ? `${result.average} Mbps` : '--';
    $('detailDuration').textContent = result?.duration ? `${result.duration} 秒` : '--';
    if (!result?.down && state?.down != null) $('detailDown').textContent = `${formatSpeed(state.down)} Mbps`;
    if (!result?.up && state?.up != null) $('detailUp').textContent = `${formatSpeed(state.up)} Mbps`;
    const tick = state?.tick;
    $('speedElapsed').textContent = result?.error ? `测速失败：${result.error}`
        : result?.duration ? '节点测速完成'
        : tick ? `${tick.phase === 'DN' ? '下行 ' : tick.phase === 'UP' ? '上行 ' : ''}${tick.elapsed.toFixed(1)} / ${tick.total.toFixed(0)} 秒` : '等待节点测速';
    drawSpeedChart();
}

function showPreferDetail(node) {
    const delay = results.get(node.id);
    const timedOut = delay !== undefined && delay < 0;
    const st = preferStats.get(node.id);
    // 与卡片渲染一致：TCPing 失败但 ICMP 在线时，明确说明是端口被拦、节点在线
    const icmpOnline = timedOut && typeof st?.icmp === 'number' && st.icmp >= 0;
    $('preferDetail').classList.remove('hidden');
    $('preferDetailName').textContent = node.name;
    $('preferDetailRegion').textContent = `${regionLabel(node.china)} · #${node.id}`;
    $('preferDetailStatus').textContent = delay === undefined
        ? '尚未完成测速'
        : (timedOut
            ? (icmpOnline ? '端口被防火墙拦截 · 节点在线（无法TCPing）' : '所有端口不可达')
            : (st?.port ? `测速已完成 · 端口 ${st.port}` : '测速已完成'));
    $('preferDelay').textContent = delay === undefined ? '--' : (timedOut ? (icmpOnline ? `ICMP ${Math.round(st.icmp)} ms` : '超时') : `${delay} ms`);
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
    if (preferRunning) return;
    if (selected.size === 0) {
        $('preferStatus').textContent = '点击卡片选择节点';
    } else if (selected.size === 1) {
        const id = [...selected][0];
        const n = nodes.find((x) => String(x.id) === id);
        $('preferStatus').innerHTML = `已选节点：<b>#${id} ${esc(n ? n.name : '')}</b>`;
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
    syncBandwidthCards();
}

$('selectAllBtn').addEventListener('click', () => {
    if (!nodes.length) return;
    if (isAllSelected()) selected.clear();
    else nodes.forEach((n) => selected.add(String(n.id)));
    saveSel();
    document.querySelectorAll('.node-card').forEach((c) =>
        c.classList.toggle('selected', selected.has(c.dataset.id)));
    updateSelUI();
    syncBandwidthCards();
});

const cardById = (id) => document.querySelector(`.node-card[data-id="${id}"]`);

// ---------- 未登录引导 ----------
// 未登录时 /nodeinfo 拿不到节点真实 IP，延迟与带宽测速都跑不起来。
// 这里在节点区顶部放一张可点击的引导卡（等同点左侧账户卡片），登录成功后自动消失。
function makeLoginHint() {
    const el = document.createElement('div');
    el.className = 'login-required';
    el.setAttribute('role', 'button');
    el.setAttribute('tabindex', '0');
    el.innerHTML = `
        <span class="login-required-icon"><svg viewBox="0 0 24 24"><circle cx="12" cy="8" r="3.4"/><path d="M5 20c0-3.6 3.1-5.7 7-5.7s7 2.1 7 5.7"/></svg></span>
        <b>点击左侧头像登录账号</b>
        <small>登录后才能获取节点真实 IP，延迟测速与带宽测速都依赖账号权限</small>`;
    const open = () => openLoginModal();
    el.addEventListener('click', open);
    el.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            open();
        }
    });
    return el;
}

function ensureLoginHint(container) {
    if (!container) return;
    const existed = container.querySelector('.login-required');
    if (qzhuaLoggedIn) {
        existed?.remove();
        return;
    }
    if (!existed) container.prepend(makeLoginHint());
}

// 未登录：清空节点数据，只留一张登录引导卡。
// 未登录时 /nodeinfo 拿不到节点真实 IP，卡片摆出来既点不了也测不了，纯属误导。
function renderLoginOnly() {
    if (qzhuaLoggedIn) return;
    nodeLoadVersion++; // 作废在途的节点请求，防止旧结果在退出登录后又写回
    nodes = [];
    results.clear();
    preferStats.clear();
    bandwidthResults.clear();
    ['totalNodes', 'bandwidthTotal'].forEach((id) => { if ($(id)) $(id).textContent = '--'; });
    ['filterCount', 'bandwidthFilterCount'].forEach((id) => { if ($(id)) $(id).textContent = '0 个节点'; });
    if ($('preferStatus')) $('preferStatus').textContent = '请先登录账号';
    if ($('currentSpeedNode')) $('currentSpeedNode').textContent = '请先登录账号';
    ['groups', 'bandwidthGroups'].forEach((id) => {
        const box = $(id);
        if (!box) return;
        box.innerHTML = '';
        box.appendChild(makeLoginHint());
    });
    updateBandwidthRunUI();
    updateExportUI();
}

// force=true 只在用户主动点「刷新」时传，用来跳过后端 5 分钟的节点缓存
async function loadNodes(force = false) {
    // 未登录直接走引导分支，不发请求、不渲染卡片
    if (!qzhuaLoggedIn) {
        renderLoginOnly();
        return;
    }
    // 不再用 nodesLoading 互斥：退出后重新登录必须能立即发起新请求，
    // 旧请求是否作废交给版本号判断。
    const version = ++nodeLoadVersion;
    $('groups').innerHTML = '<div class="empty-state"><span class="loader"></span><b>正在加载节点</b><small>请稍候</small></div>';
    $('bandwidthGroups').innerHTML = '<div class="empty-state"><span class="loader"></span><b>正在加载节点</b><small>请稍候</small></div>';
    $('preferStatus').textContent = '节点加载中…';

    let loaded;
    try {
        loaded = await GetNodes('all', force);
    } catch (err) {
        // 失败时也要确认自己仍是当前请求，否则会把新登录的界面覆盖成错误态
        if (version !== nodeLoadVersion || !qzhuaLoggedIn) return;
        $('groups').innerHTML = `<div class="empty-state"><b>节点加载失败</b><small>${esc(err)}</small></div>`;
        $('bandwidthGroups').innerHTML = `<div class="empty-state"><b>节点加载失败</b><small>${esc(err)}</small></div>`;
        $('preferStatus').textContent = '节点拉取失败';
        ensureLoginHint($('groups'));
        ensureLoginHint($('bandwidthGroups'));
        return;
    }
    // 请求期间退出登录或又发起了新一轮加载：直接丢弃这次结果
    if (version !== nodeLoadVersion || !qzhuaLoggedIn) return;

    nodes = Array.isArray(loaded) ? loaded : [];
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

$('preferNodeSearch').addEventListener('input', debounce((event) => {
    preferNodeQuery = event.target.value;
    buildGroups();
}));

$('bandwidthNodeSearch').addEventListener('input', debounce((event) => {
    bandwidthNodeQuery = event.target.value;
    buildBandwidthGroups();
}));

// 两个视图共用同一份 nodes：刷新会整体替换列表，而正在跑的测速任务仍按旧列表
// 继续执行，于是卡片已经换成新节点、任务却还在报旧节点的进度，界面与任务对不上。
// 所以任一测速在跑时两个刷新按钮都要禁用，而不是各看各的状态。
function syncRefreshButtons() {
    const busy = preferRunning || bandwidthRunning;
    ['refreshBtn', 'bandwidthRefreshBtn'].forEach((id) => {
        const btn = $(id);
        if (!btn) return;
        btn.disabled = busy;
        btn.title = busy ? '测速进行中，结束后可刷新' : '重新拉取节点列表';
    });
}

// 节点列表被整体替换后，旧的测速结果不再对应确定的节点：三处结果容器一起清空，
// 否则会出现「延迟已经是新值，发送次数/丢包率还停在上一轮」这种自相矛盾的卡片。
function clearTestResults() {
    results.clear();
    preferStats.clear();
    bandwidthResults.clear();
    hidePreferLeaderboard();
    $('bandwidthLeaderboard')?.classList.add('hidden');
    updateExportUI();
}

function refreshNodes(fromBandwidth) {
    // 按钮此时通常已被禁用，这里再挡一次：状态同步万一有延迟也不会真的发出请求。
    if (preferRunning || bandwidthRunning) {
        const msg = '测速进行中，已忽略本次节点刷新';
        if (fromBandwidth) appendSpeedLog('warn', msg);
        else appendLog('prefer', 'warn', msg);
        return;
    }
    if (!qzhuaLoggedIn) {
        openLoginModal();
        return;
    }
    clearTestResults();
    loadNodes(true);
}

$('refreshBtn').addEventListener('click', () => refreshNodes(false));
$('bandwidthRefreshBtn').addEventListener('click', () => refreshNodes(true));
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
let preferTotal = 0;

function setPreferBtn(running, loading = false) {
    const btn = $('preferBtn');
    setTestButtonState(btn, running ? (loading ? '准备中…' : '测速中…') : '开始测速', running && loading, running && !loading);
    btn.disabled = running;
    btn.classList.remove('speed-resume');
    $('preferCancelBtn').classList.toggle('hidden', !running);
    updateExportUI();
}

onAppEvent('preferEvent', (ev) => {
    switch (ev.kind) {
        case 'start':
            preferRunning = true;
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
            syncRefreshButtons();
            closeExportMenu('prefer');
            $('preferStatus').textContent = '准备测速';
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
            preferStats.set(ev.nodeId, { sent: ev.sent, recv: ev.recv, loss: ev.loss, port: ev.port, icmp: ev.icmpDelay });
            // TCPing 失败但 ICMP 在线 = 节点活着、端口被防火墙拦截，与真超时区分展示
            const icmpOnline = ev.delay < 0 && typeof ev.icmpDelay === 'number' && ev.icmpDelay >= 0;
            $('preferProgressFill').style.width = `${Math.round(ev.index / ev.total * 100)}%`;
            $('preferStatus').textContent = `${ev.name} · ${ev.delay < 0
                ? (icmpOnline ? `端口拦截 · 节点在线 ${Math.round(ev.icmpDelay)}ms` : '超时')
                : `${ev.delay}ms`}`;
            const card = cardById(ev.nodeId);
            if (card) {
                card.classList.remove('testing');
                const delayValue = card.querySelector('[data-delay-value]');
                if (delayValue) {
                    delayValue.textContent = ev.delay < 0
                        ? (icmpOnline ? `ICMP ${Math.round(ev.icmpDelay)}ms` : '超时')
                        : `${ev.delay}ms`;
                    delayValue.className = `delay-value ${ev.delay < 0 ? (icmpOnline ? 'medium' : 'slow') : delayTone(ev.delay)}`;
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
                appendLog('prefer', 'ok', `#${ev.nodeId} ${ev.name} · 延迟 ${ev.delay}ms · 端口 ${ev.port}`);
            } else if (icmpOnline) {
                appendLog('prefer', 'warn', `#${ev.nodeId} ${ev.name} · 端口被防火墙拦截，节点在线（ICMP ${Math.round(ev.icmpDelay)}ms）· 无法建隧道，不参与排名`);
            } else {
                appendLog('prefer', 'err', `#${ev.nodeId} ${ev.name} · 超时（端口不通且 ping 无响应，节点可能离线）`);
            }
            break;
        }

        case 'finish': {
            preferRunning = false;
            setPreferBtn(false);
            syncRefreshButtons();
            document.querySelectorAll('.node-card').forEach((c) => c.classList.remove('testing'));
            if (ev.result && ev.result.length) {
                renderPreferLeaderboard(ev.result);
                const order = new Map(ev.result.map((item, index) => [item.id, index]));
                nodes.sort((a, b) => (order.get(a.id) ?? Number.MAX_SAFE_INTEGER) - (order.get(b.id) ?? Number.MAX_SAFE_INTEGER));
                buildGroups();
                const best = ev.result[0];
                $('preferStatus').innerHTML =
                `测速完成 · 最快：<b>#${best.id} ${esc(best.name)}</b> <span class="ok">${best.delay}ms</span> · 共 ${ev.total} 个节点`;
                appendLog('prefer', 'ok', `测速完成 · ${ev.result.length}/${ev.total} 个节点可用 · 最快 #${best.id} ${best.name} ${best.delay}ms`);
            } else {
                $('preferStatus').textContent = '测速完成';
                appendLog('prefer', 'warn', `测速完成 · ${ev.total} 个节点全部超时，无可用结果`);
            }
            $('preferProgressFill').style.width = '100%';
            break;
        }

        case 'needLogin':
            // 后端确认拿不到节点 IP 且未登录：这不是“取消”，要引导去登录
            preferRunning = false;
            setPreferBtn(false);
            syncRefreshButtons();
            document.querySelectorAll('.node-card.testing').forEach((c) => c.classList.remove('testing'));
            $('preferStatus').textContent = '请先登录账号';
            $('preferProgressFill').style.width = '0%';
            appendLog('prefer', 'warn', '未登录：延迟测速需要登录后获取节点真实 IP');
            renderLoginOnly();
            openLoginModal();
            break;

        case 'error':
            preferRunning = false;
            setPreferBtn(false);
            syncRefreshButtons();
            document.querySelectorAll('.node-card.testing').forEach((c) => c.classList.remove('testing'));
            $('preferStatus').textContent = '测速无法开始';
            $('preferProgressFill').style.width = '0%';
            appendLog('prefer', 'err', ev.name || '测速无法开始');
            break;

        case 'cancel':
            preferRunning = false;
            setPreferBtn(false);
            syncRefreshButtons();
            document.querySelectorAll('.node-card.testing').forEach((c) => c.classList.remove('testing'));
            $('preferStatus').textContent = '测速已取消';
            $('preferProgressFill').style.width = '0%';
            appendLog('prefer', 'warn', '测速已取消');
            break;
    }
});

$('preferBtn').addEventListener('click', async () => {
    // 运行中按钮已禁用，暂停也已移除；这里只处理“开始”。
    if (preferRunning) return;
    if (!qzhuaLoggedIn) {
        $('preferStatus').textContent = '请先登录账号';
        appendLog('prefer', 'warn', '未登录：延迟测速需要登录后获取节点真实 IP');
        renderLoginOnly();
        openLoginModal();
        return;
    }
    if (!nodes.length) return;
    preferRunning = true;
    setPreferBtn(true, true);
    syncRefreshButtons();
    $('preferStatus').textContent = '准备测速';
    appendLog('prefer', 'info', selected.size ? `准备测速已选 ${selected.size} 个节点` : '准备测速全部节点');
    try {
        await StartPrefer(selected.size ? [...selected].map(Number) : []);
    } catch (err) {
        preferRunning = false;
        setPreferBtn(false);
        syncRefreshButtons();
        $('preferStatus').textContent = '测速启动失败';
        appendLog('prefer', 'err', '启动延迟测速失败：' + err);
    }
});
$('preferCancelBtn').addEventListener('click', async () => {
    const btn = $('preferCancelBtn');
    // 与带宽测速的停止按钮保持一致：请求在途时重复点击没有意义
    if (btn.disabled) return;
    btn.disabled = true;
    appendLog('prefer', 'warn', '用户点击停止测速');
    try {
        await CancelPrefer();
    } catch (err) {
        // 取消失败时不要动 preferRunning：界面继续显示「测速中」才是真实状态，
        // 按钮恢复可点，用户可以再试一次。
        appendLog('prefer', 'err', '停止延迟测速失败：' + err);
    } finally {
        btn.disabled = false;
    }
});

// 两个导出按钮共用同一套可用性规则：
// 节点列表加载后即可导出（延迟/带宽报告都包含节点清单）；测速过程中先禁用，
// 避免导出到一半的数据。这样登录/刷新/测速结束后不会出现两个页面状态不一致。
function updateExportUI() {
    const preferBtn = $('preferExportBtn');
    if (preferBtn) {
        preferBtn.disabled = nodes.length === 0 || preferRunning;
        preferBtn.title = nodes.length === 0
            ? '请先刷新加载节点'
            : (preferRunning ? '测速结束后可导出' : '导出节点列表与延迟结果');
    }
    const bandwidthBtn = $('bandwidthExportBtn');
    if (bandwidthBtn) {
        bandwidthBtn.disabled = nodes.length === 0 || speedRunning;
        bandwidthBtn.title = nodes.length === 0
            ? '请先刷新加载节点'
            : (speedRunning ? '测速结束后可导出' : '导出节点列表与带宽结果');
    }
}

// ---------- 导出（节点列表 + 延迟测速结果）：CSV / 图片 ----------
// 导出结果一律按实测延迟排序，而不是节点编号顺序：
// 图片只截前 12 行，按编号排就成了「编号最小的 12 个」，跟快慢无关；
// CSV 虽是全量清单，但用户看的是「哪个节点最快」，编号序同样不直观。

// CSV 单元格统一处理：引号转义 + 防电子表格公式注入。
// 节点名称、区域等文本来自远端接口，不能假设它们不会以 = + - @ 开头。
const CSV_FORMULA = /^[\u0000-\u0020]*[=+\-@]/;
const csvCell = (value) => {
    let text = String(value ?? '');
    if (CSV_FORMULA.test(text)) text = "'" + text;
    return `"${text.replace(/"/g, '""')}"`;
};
const toCsvText = (rows) => '\uFEFF' + rows.map((r) => r.map(csvCell).join(',')).join('\r\n');

const preferExportRows = () => {
    // 只让「测到了延迟」的节点参与排名：超时节点在 results 里是 -1，
    // 若直接排序会拿到第 1 名（-1 最小），导出表就出现"-1ms 排名 1"的假数据。
    const sorted = [...results.entries()].filter(([, d]) => d >= 0).sort((a, b) => a[1] - b[1]);
    const rank = new Map(sorted.map(([id], i) => [id, i + 1]));
    // 排序键：0=测到延迟（按延迟升序）；1=TCPing 超时但 ICMP 在线（按 ICMP 升序）；
    // 2=超时/未测。同组内再按编号兜底，保证多次导出的顺序稳定。
    const speedKey = (n) => {
        const d = results.get(n.id);
        if (typeof d === 'number' && d >= 0) return [0, d];
        const icmp = preferStats.get(n.id)?.icmp;
        if (typeof icmp === 'number' && icmp >= 0) return [1, icmp];
        return [2, 0];
    };
    const list = nodes.slice().sort((a, b) => {
        const [ga, va] = speedKey(a);
        const [gb, vb] = speedKey(b);
        return ga !== gb ? ga - gb : (va !== vb ? va - vb : a.id - b.id);
    });
    const rows = [['编号', '名称', '区域', '节点组', '地域', '建站', 'UDP', '防御', '延迟(ms)', '延迟排名']];
    for (const n of list) {
        const d = results.get(n.id);
        const st = preferStats.get(n.id);
        // TCPing 超时但 ICMP 在线的节点，在导出里也注明，避免被当成节点离线
        const icmpOnline = d !== undefined && d < 0 && typeof st?.icmp === 'number' && st.icmp >= 0;
        rows.push([
            n.id, n.name, n.area,
            n.group === 'vip' ? '会员' : '免费',
            n.china === 'yes' ? '国内' : '国外',
            n.web === 'yes' ? '是' : '否',
            n.udp ? '是' : '否',
            n.fangyu ? '是' : '否',
            d === undefined ? '' : (d < 0 ? (icmpOnline ? `超时(ICMP ${Math.round(st.icmp)}ms在线)` : '超时') : d),
            rank.has(n.id) ? rank.get(n.id) : '',
        ]);
    }
    return rows;
};

const exportPreferCsv = async () => {
    if (!nodes.length) return;
    const rows = preferExportRows();
    const csv = toCsvText(rows);
    try {
        const path = await SaveCSV(csv);
        if (path) $('preferStatus').innerHTML = `已导出：<b>${esc(path)}</b>`;
    } catch (err) {
        $('preferStatus').textContent = '导出失败：' + err;
    }
};

// 导出 PNG 的 canvas 字体栈：必须与页面字体栈保持一致，
// 否则 mac/Linux 上 "Microsoft YaHei" 不存在，中文会整片掉成方块。
const CANVAS_FONT = '"Microsoft YaHei","PingFang SC","Hiragino Sans GB","Noto Sans CJK SC","Source Han Sans SC",sans-serif';

// ---------- 两份 PNG 报告共用的排版骨架 ----------
// 画布尺寸、底色、标题、副标题、表头位置、表格行距都写在这里，
// 两个导出只负责自己的列定义、单元格取值与图表内容。
const REPORT_SIZE = { w: 980, h: 640, tableTop: 140, rowHeight: 22, maxRows: 12, colsLeft: 36 };

function createReportCanvas({ title, subtitle, cols, headers }) {
    const { w, h } = REPORT_SIZE;
    const canvas = document.createElement('canvas');
    canvas.width = w;
    canvas.height = h;
    const ctx = canvas.getContext('2d');
    const css = getComputedStyle(document.documentElement);
    const color = (name, fallback) => (css.getPropertyValue(name).trim() || fallback);
    ctx.fillStyle = color('--bg', '#0d0f12');
    ctx.fillRect(0, 0, w, h);
    ctx.fillStyle = color('--text', '#f2f4f7');
    ctx.font = `600 20px ${CANVAS_FONT}`;
    ctx.fillText(title, REPORT_SIZE.colsLeft, 48);
    ctx.fillStyle = color('--muted', '#8b939e');
    ctx.font = `12px ${CANVAS_FONT}`;
    ctx.fillText(subtitle, REPORT_SIZE.colsLeft, 74);
    ctx.fillStyle = color('--dim', '#5c6570');
    ctx.font = `11px ${CANVAS_FONT}`;
    headers.forEach((t, i) => ctx.fillText(t, cols[i], 108));
    ctx.strokeStyle = color('--line', '#292e36');
    ctx.beginPath();
    ctx.moveTo(REPORT_SIZE.colsLeft, 116);
    ctx.lineTo(w - REPORT_SIZE.colsLeft, 116);
    ctx.stroke();
    return { canvas, ctx, color, w, h };
}

// 表格区：rows 是导出二维数组（第 0 行表头），cell(行) 负责转成单元格文本
function drawReportTable(ctx, color, cols, rows, cell) {
    ctx.font = `12px ${CANVAS_FONT}`;
    rows.slice(1, REPORT_SIZE.maxRows + 1).forEach((r, idx) => {
        const y = REPORT_SIZE.tableTop + idx * REPORT_SIZE.rowHeight;
        ctx.fillStyle = color('--text', '#f2f4f7');
        cell(r).forEach((t, i) => ctx.fillText(t, cols[i], y));
    });
}

const exportPreferPng = async () => {
    if (!nodes.length) return;
    const rows = preferExportRows();
    const tested = [...results.keys()].length;
    const cols = [36, 96, 330, 430, 510, 590, 670, 760, 850];
    const { canvas, ctx, color, w } = createReportCanvas({
        title: 'ChmlFrp 延迟测速报告',
        subtitle: `测速时间：${new Date().toLocaleString('zh-CN')}　节点数：${nodes.length}　已测：${tested}　按延迟从低到高排序`,
        cols,
        headers: ['编号', '名称', '区域', '节点组', '地域', 'UDP', '防御', '延迟', '排名'],
    });
    drawReportTable(ctx, color, cols, rows, (r) => [
        `#${r[0]}`, String(r[1]).slice(0, 18), String(r[2]).slice(0, 10), String(r[3]), String(r[4]),
        String(r[6]), String(r[7]),
        r[8] === '' ? '--' : `${r[8]} ms`, r[9] === '' ? '--' : `#${r[9]}`,
    ]);
    // 延迟对比柱状图（取已测节点中延迟最低的前 10 个）
    const chartTop = 430;
    const chartH = 170;
    ctx.fillStyle = color('--dim', '#5c6570');
    ctx.font = `11px ${CANVAS_FONT}`;
    ctx.fillText('延迟对比（ms，取延迟最低的前 10 个节点）', 36, chartTop - 12);
    ctx.strokeStyle = color('--line', '#292e36');
    ctx.strokeRect(36, chartTop, w - 72, chartH);
    // 只取真正测到延迟的节点：超时节点记作 -1，不过滤会排在队首、画出 -1 的柱子
    const ranked = [...results.entries()].filter(([, d]) => d >= 0).sort((a, b) => a[1] - b[1]).slice(0, 10);
    if (!ranked.length) {
        ctx.fillStyle = color('--muted', '#8b939e');
        ctx.font = `12px ${CANVAS_FONT}`;
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
            ctx.font = `11px ${CANVAS_FONT}`;
            ctx.fillText(`${delay}`, x + 6, y - 6);
            ctx.fillStyle = color('--dim', '#5c6570');
            ctx.font = `10px ${CANVAS_FONT}`;
            ctx.fillText(String(node?.name ?? `#${id}`).slice(0, 8), x + 6, chartTop + chartH - 8);
        });
    }
    try {
        const path = await SaveImage(canvas.toDataURL('image/png').split(',')[1]);
        if (path) $('preferStatus').innerHTML = `已导出：<b>${esc(path)}</b>`;
    } catch (err) {
        $('preferStatus').textContent = '导出失败：' + err;
    }
};

$('closeBandwidthDetail').addEventListener('click', () => $('bandwidthDetail').classList.add('hidden'));

// ---------- 测速任务弹窗（静态确认信息，不做动态刷新） ----------
$('closeSpeedTask').addEventListener('click', () => $('speedTaskModal').classList.add('hidden'));
$('taskCloseBtn').addEventListener('click', () => $('speedTaskModal').classList.add('hidden'));

// ---------- 导出带宽测速结果（CSV / 图片） ----------
// 与延迟测速一致：加载节点后即可导出；未测速的节点保留在清单里，结果列留空。
const hasBandwidthResult = (r) => Boolean(r && (r.down !== undefined || r.up !== undefined || r.delay !== undefined));

const bandwidthExportRows = () => {
    const ts = new Date().toLocaleString('zh-CN');
    const rows = [['编号', '名称', '节点组', '地域', '延迟(ms)', '下载(Mbps)', '上传(Mbps)', '平均(Mbps)', '测速时长(秒)', '测速时间']];
    // 与延迟导出一致：一律按下载速度从高到低排（跟界面带宽排行默认维度一致），
    // 未测/失败的垫底，同值按编号兜底。
    const downOf = (n) => {
        const r = bandwidthResults.get(String(n.id));
        if (!hasBandwidthResult(r) || r.error) return -1;
        const v = Number(r.down);
        return Number.isFinite(v) ? v : -1;
    };
    const list = nodes.slice().sort((a, b) => {
        const va = downOf(a);
        const vb = downOf(b);
        if (va < 0 && vb < 0) return a.id - b.id;
        if (va < 0) return 1;
        if (vb < 0) return -1;
        return vb !== va ? vb - va : a.id - b.id;
    });
    for (const n of list) {
        const r = bandwidthResults.get(String(n.id));
        const tested = hasBandwidthResult(r);
        rows.push([
            n.id, n.name,
            n.group === 'vip' ? '会员' : '普通',
            regionLabel(n.china),
            tested ? (r.delay ?? '') : '',
            tested ? (r.down ?? '') : '',
            tested ? (r.up ?? '') : '',
            tested ? (r.average ?? '') : '',
            tested ? (r.duration ?? '') : '',
            tested ? ts : '',
        ]);
    }
    return rows;
};

const exportBandwidthCsv = async () => {
    if (!nodes.length) return;
    const rows = bandwidthExportRows();
    try {
        const path = await SaveCSV(toCsvText(rows));
        if (path) {
            $('currentSpeedNode').textContent = `已导出：${path}`;
            appendLog('bandwidth', 'ok', `已导出 CSV · ${path}`);
        }
    } catch (err) {
        $('currentSpeedNode').textContent = '导出失败';
        appendLog('bandwidth', 'err', `导出 CSV 失败 · ${err}`);
    }
};

const exportBandwidthPng = async () => {
    if (!nodes.length) return;
    const rows = bandwidthExportRows();
    const tested = nodes.filter((n) => hasBandwidthResult(bandwidthResults.get(String(n.id)))).length;
    const cols = [36, 96, 330, 390, 470, 560, 650, 740];
    const { canvas, ctx, color, w } = createReportCanvas({
        title: 'ChmlFrp 带宽测速报告',
        subtitle: `测速时间：${new Date().toLocaleString('zh-CN')}　节点数：${nodes.length}　已测：${tested}　单次时长：${settings.testSeconds} 秒　按下载速度从高到低排序`,
        cols,
        headers: ['编号', '名称', '节点组', '地域', '延迟', '下载', '上传', '平均'],
    });
    drawReportTable(ctx, color, cols, rows, (r) => [
        `#${r[0]}`, String(r[1]).slice(0, 18), String(r[2]), String(r[3]),
        r[4] === '' ? '--' : `${r[4]} ms`, r[5] === '' ? '--' : `${r[5]} Mbps`,
        r[6] === '' ? '--' : `${r[6]} Mbps`, r[7] === '' ? '--' : `${r[7]} Mbps`,
    ]);
    const chartTop = 430;
    const chartH = 160;
    ctx.fillStyle = color('--dim', '#5c6570');
    ctx.font = `11px ${CANVAS_FONT}`;
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
        ctx.fillText(tested ? '暂无速率曲线数据' : '尚未测速，表格仅包含节点列表', 36, chartTop + chartH / 2);
    }
    try {
        const path = await SaveImage(canvas.toDataURL('image/png').split(',')[1]);
        if (path) {
            $('currentSpeedNode').textContent = `已导出：${path}`;
            appendLog('bandwidth', 'ok', `已导出图片 · ${path}`);
        }
    } catch (err) {
        $('currentSpeedNode').textContent = '导出失败';
        appendLog('bandwidth', 'err', `导出图片失败 · ${err}`);
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
    get v() {
        const v = readStorage('appTheme');
        return v === 'system' || v === 'light' || v === 'dark' ? v : 'dark';
    },
    set v(value) { writeStorage('appTheme', value); },
};
const manualTheme = {
    get v() {
        const v = readStorage('appThemeManual');
        return v === 'light' || v === 'dark' ? v : 'dark';
    },
    set v(value) { writeStorage('appThemeManual', value); },
};
const themeMQ = window.matchMedia('(prefers-color-scheme: light)');
const applyTheme = () => {
    const mode = themeMode.v;
    const resolved = mode === 'system' ? (themeMQ.matches ? 'light' : 'dark') : mode;
    document.documentElement.dataset.theme = resolved;
    // 曲线颜色取自 CSS 变量，切主题后要立刻重画一次，否则要等下一帧采样才更新
    drawSpeedChart();
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
// 初始值由 GetAutostart 异步读回。读取期间禁用开关，避免「读取结果晚到、
// 把用户刚点开的开关又覆盖回去」这种竞争；busy 标记是第二道保险，
// 万一某些平台在禁用状态下仍派发了 change 事件，也不会被回读结果覆盖。
let autostartReady = false;
let autostartBusy = false;
const autostartEl = $('setAutostart');
autostartEl.disabled = true;

$('setAutostart').addEventListener('change', async (e) => {
    autostartBusy = true;
    e.target.disabled = true;
    try {
        await SetAutostart(e.target.checked);
        $('autostartHint').textContent = e.target.checked ? '已开启开机自启' : '已关闭开机自启';
    } catch (err) {
        e.target.checked = !e.target.checked;
        $('autostartHint').textContent = String(err);
    } finally {
        autostartBusy = false;
        if (autostartReady) e.target.disabled = false;
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
    const observer = new MutationObserver(syncAd);
    observer.observe(board, { attributes: true, attributeFilter: ['class'] });
    // 观察者也要登记：退出时统一 disconnect，否则会一直持有 board 的引用
    adObservers.push(observer);
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

// ---------- 本机 IP：网卡地址枚举 + 公网 IP 信息（ipwho.is） ----------
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

// 回环与内网地址没有公网归属地，v4/v6 公网地址都可查。
// 不能只对 IPv4 判 private：IPv6 的 ULA(fc00::/7) 同样是内网，
// 后端已按协议族分别标记，这里只看标记本身。
const ipLookupable = (x) => !x.loopback && !x.private;
const ipTagText = (x) => (x.loopback ? '回环' : (x.private ? '内网' : (x.version === 6 ? 'IPv6' : '公网')));
const ipTagClass = (x) => (x.loopback ? 'loop' : (x.private ? 'lan' : (x.version === 6 ? 'v6' : 'wan')));

function renderLocalIPs(ips) {
    const list = $('localIpList');
    $('toolIpv4Count').textContent = ips.filter((x) => x.version === 4).length;
    $('toolIpv6Count').textContent = ips.filter((x) => x.version === 6).length;
    if (!ips.length) {
        list.innerHTML = '<div class="empty-state compact"><b>没有可用地址</b><small>网卡可能全部未启用</small></div>';
        return;
    }
    list.innerHTML = ips.map((x) => {
        const canLookup = ipLookupable(x);
        return `
        <div class="ip-row" data-ip="${esc(x.ip)}" data-lookup="${canLookup ? '1' : '0'}">
            <div class="ip-main"><b>${esc(x.ip)}</b><small>${esc(x.iface)} · IPv${x.version}</small></div>
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
        $('localIpList').innerHTML = `<div class="empty-state compact"><b>读取网卡失败</b><small>${esc(err)}</small></div>`;
    }
}

// 渲染 IP 信息卡片（self=true 表示当前出口 IP，否则为手动查询结果）
function renderIPGeo(geo, self) {
    const card = $('ipInfoCard');
    const place = [geo.region, geo.city].filter(Boolean).join(' · ') || '地理位置未知';
    const withCode = (name, code) => (name ? `${name}${code ? ` (${code})` : ''}` : '--');
    // 0 是合法坐标（赤道 / 本初子午线），用 || 判空会把 0 当成"没有坐标"；
    // ipwho.is 查不到时给的是 0,0，所以只把两者同时为 0 视为未知。
    const hasCoord = Number.isFinite(geo.latitude) && Number.isFinite(geo.longitude)
        && (geo.latitude !== 0 || geo.longitude !== 0);
    const coord = hasCoord
        ? `${Number(geo.latitude).toFixed(4)}, ${Number(geo.longitude).toFixed(4)}`
        : '--';
    // 时区额外显示缩写、UTC 偏移与夏令时标记
    const tzExtra = [geo.timeZoneAbbr, geo.utcOffset ? `UTC${geo.utcOffset}` : '', geo.isDST ? '夏令时' : '']
        .filter(Boolean).join(' · ');
    // 运营商格子：组织名与运营商不同时，补一行组织名（如 中国移动 集团 vs 分公司）
    const ispExtra = geo.org && geo.org !== geo.isp ? geo.org : '';
    const cells = [
        ['大洲', withCode(geo.continent, geo.continentCode)],
        ['国家/地区', withCode(geo.country, geo.countryCode)],
        ['省 / 州', withCode(geo.region, geo.regionCode)],
        ['城市', geo.city || '--'],
        ['邮编', geo.zip || '--'],
        ['电话区号', geo.callingCode ? `+${geo.callingCode}` : '--'],
        ['首都', geo.capital || '--'],
        ['ASN', geo.asn || '--'],
        ['运营商', geo.isp || '--', ispExtra],
        ['网络域名', geo.domain || '--'],
        ['时区', geo.timeZone || '--', tzExtra],
        ['经纬度', coord],
    ];
    const flag = geo.flagImg
        ? `<img class="ip-flag" src="${esc(geo.flagImg)}" alt="" loading="lazy"/>`
        : '';
    const badges = [
        geo.country ? `<span class="ip-badge">${flag}${esc(geo.country)}</span>` : '',
        geo.isEU ? '<span class="ip-badge">欧盟</span>' : '',
    ].filter(Boolean).join('');
    card.className = 'ip-geo has-data';
    card.innerHTML = `
        <div class="ip-geo-head">
            <div class="ip-geo-title"><b>${esc(geo.ip)}</b>${badges}</div>
            <div class="ip-geo-sub"><span class="ip-geo-place">${esc(place)}</span><span class="ip-geo-kind">IPv${geo.version || 4}${self ? ' · 当前出口' : ' · 指定查询'}</span></div>
        </div>
        <div class="ip-geo-grid">${cells.map(([k, v, extra]) => `<div><span>${k}</span><b>${esc(v)}</b>${extra ? `<em>${esc(extra)}</em>` : ''}</div>`).join('')}</div>
        <p class="ip-geo-note">数据来源 ipwho.is · 优先 IPv6 · 免密钥 · 1000 次/天 · 中文结果</p>`;
    // 旗帜是远程 SVG，加载失败就摘掉，避免显示破图空框
    card.querySelectorAll('.ip-flag').forEach((img) => {
        img.addEventListener('error', () => { img.remove(); });
    });
    $('myIpBackBtn').classList.toggle('hidden', self);
}

function renderIPGeoLoading(text) {
    const card = $('ipInfoCard');
    card.className = 'ip-geo is-loading';
    card.innerHTML = `<div class="ip-geo-empty"><span class="loader"></span><b>${esc(text)}</b></div>`;
}

function renderIPGeoError(title, msg) {
    const card = $('ipInfoCard');
    card.className = 'ip-geo has-error';
    card.innerHTML = `<div class="ip-geo-empty"><b>${esc(title)}</b><small>${esc(msg)}</small></div>`;
}

const stripErr = (err) => String(err).replace(/^Error:\s*/, '');

// IP 查询是「后发先至」的典型场景：连点刷新、或点完某个地址的归属地又点刷新，
// 先发的请求可能后返回，把新结果盖成旧地址。用递增序号只让最后一次落地，
// 再用在途计数管理按钮，避免被取代的请求提前把按钮解禁。
let ipQuerySeq = 0;
let ipQueryInflight = 0;
const beginIPQuery = () => {
    ipQueryInflight += 1;
    return ++ipQuerySeq;
};
const endIPQuery = () => {
    ipQueryInflight = Math.max(0, ipQueryInflight - 1);
    if (ipQueryInflight === 0) $('ipQueryBtn').disabled = false;
};
const isLatestIPQuery = (seq) => seq === ipQuerySeq;

// 自动获取当前出口 IP 信息
async function loadMyIP() {
    const seq = beginIPQuery();
    renderIPGeoLoading('正在获取当前 IP 信息');
    $('toolQueryState').textContent = '获取中…';
    try {
        const geo = await GetMyIPInfo();
        if (!isLatestIPQuery(seq)) return; // 已被更新的查询取代，丢弃这次结果
        renderIPGeo(geo, true);
        $('toolQueryState').textContent = '已获取';
    } catch (err) {
        if (!isLatestIPQuery(seq)) return;
        renderIPGeoError('获取当前 IP 失败', stripErr(err));
        $('toolQueryState').textContent = '获取失败';
    } finally {
        endIPQuery();
    }
}

async function queryIP(inputIP) {
    const ip = String(inputIP ?? $('ipQueryInput').value).trim();
    if (!ip) {
        $('toolQueryState').textContent = '请输入 IP';
        return;
    }
    const btn = $('ipQueryBtn');
    const seq = beginIPQuery();
    btn.disabled = true;
    $('toolQueryState').textContent = '查询中…';
    renderIPGeoLoading(`正在查询 ${ip}`);
    try {
        const geo = await QueryIPInfo(ip);
        if (!isLatestIPQuery(seq)) return;
        renderIPGeo(geo, false);
        $('toolQueryState').textContent = '已查询';
    } catch (err) {
        if (!isLatestIPQuery(seq)) return;
        renderIPGeoError('查询失败', stripErr(err));
        $('myIpBackBtn').classList.remove('hidden');
        $('toolQueryState').textContent = '查询失败';
    } finally {
        endIPQuery();
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

// ---------- 自动更新 ----------
// 后端是唯一状态源：检查、下载、校验、暂存都在 Go 侧完成，
// 前端只渲染 updateState 快照并触发动作，避免两套状态互相打架。
// 启动即检查，不按时间间隔节流：被跳过的版本由后端静默忽略，
// 因此每次启动最多只是多一次 api.github.com 请求。
let updateSnapshot = { status: 'idle', currentVersion: '' };

const formatSize = (bytes) => {
    if (!bytes || bytes <= 0) return '0 MB';
    const mb = bytes / 1048576;
    return `${mb >= 100 ? mb.toFixed(0) : mb.toFixed(1)} MB`;
};
const updateRatio = (done, total) => (total > 0 ? Math.min(1, Math.max(0, done / total)) : 0);

const updateSettings = document.createElement('div');
updateSettings.className = 'settings-group';
updateSettings.innerHTML = `<div class="settings-group-head">更新</div>
    <div class="settings-card">
        <div class="setting-row">
            <div class="setting-text"><b>当前版本</b><small id="updateSettingsStatus">--</small></div>
            <button id="checkUpdateBtn" class="btn secondary" type="button">检查更新</button>
        </div>
        <div class="setting-row">
            <div class="setting-text"><b>启动时自动检查更新</b><small>每次启动都会检查；发现新版本时提示，可跳过指定版本。</small></div>
            <input id="setAutoUpdate" type="checkbox" class="switch"/>
        </div>
    </div>`;
document.querySelector('#view-settings .settings-list').append(updateSettings);

function renderUpdateState(st) {
    if (!st) return;
    updateSnapshot = st;
    const info = st.info || {};
    const status = st.status || 'idle';
    const current = String(st.currentVersion || '').replace(/^v/i, '');
    const version = String(info.version || '').replace(/^v/i, '');

    if ($('updateSettingsStatus') && status !== 'checking') {
        $('updateSettingsStatus').textContent = current ? `当前版本 v${current}` : '--';
    }
    if (current) $('updateCurrent').textContent = `当前版本 v${current}`;
    if (!['available', 'downloading', 'ready', 'error'].includes(status)) return;

    if (version) $('updateVersion').textContent = `v${version}`;
    const notes = $('updateNotes');
    notes.textContent = info.notes || '（该版本未提供更新说明）';
    notes.classList.toggle('hidden', status !== 'available');

    const progress = $('updateProgress');
    const hint = $('updateHint');
    const action = $('updateAction');
    const later = $('updateLater');
    const skip = $('updateSkip');
    const badge = $('updateBadge');

    action.classList.add('primary');
    action.classList.remove('secondary');
    action.disabled = false;
    progress.classList.toggle('hidden', status !== 'downloading');
    later.classList.remove('hidden');
    skip.classList.toggle('hidden', status !== 'available');

    if (status === 'available') {
        // 安装版（setup）会跑安装包静默安装，绿色版是解压覆盖，文案要区分开，
        // 否则用户以为只是换个 exe，结果看到安装程序在动会困惑。
        const setupMode = info.installMode === 'setup';
        badge.textContent = '发现新版本';
        hint.textContent = !info.canAutoInstall
            ? '当前平台暂不支持自动安装，将打开下载页面。'
            : (setupMode ? '将下载安装包并静默安装，完成后自动重启。' : '下载完成后重启软件即可完成更新。');
        action.textContent = info.canAutoInstall ? (setupMode ? '下载并安装' : '立即更新') : '前往下载';
    } else if (status === 'downloading') {
        badge.textContent = '正在下载更新';
        const ratio = updateRatio(st.downloaded, st.total);
        hint.textContent = `${formatSize(st.downloaded)} / ${formatSize(st.total)} · ${Math.round(ratio * 100)}%`;
        progress.value = ratio;
        action.textContent = '取消下载';
        action.classList.remove('primary');
        action.classList.add('secondary');
    } else if (status === 'ready') {
        badge.textContent = '更新已准备就绪';
        hint.textContent = info.installMode === 'setup'
            ? '将运行安装包完成安装，随后自动重启。'
            : '重启软件即可完成安装，也可以稍后手动重启。';
        action.textContent = '立即重启';
    } else {
        badge.textContent = '更新失败';
        hint.textContent = st.error || '更新过程中出现错误，请重试。';
        action.textContent = '重试';
    }
    $('updateModal').classList.remove('hidden');
}

onAppEvent('updateState', renderUpdateState);
onAppEvent('updateProgress', (ev) => {
    if (updateSnapshot.status !== 'downloading') return;
    const done = ev.downloaded || 0;
    const total = ev.total || updateSnapshot.total || 0;
    updateSnapshot.downloaded = done;
    if (total) updateSnapshot.total = total;
    const ratio = updateRatio(done, total);
    $('updateProgress').value = ratio;
    $('updateHint').textContent = `${formatSize(done)} / ${formatSize(total)} · ${Math.round(ratio * 100)}%`;
});

$('updateAction').addEventListener('click', async () => {
    const st = updateSnapshot;
    const info = st.info || {};
    try {
        if (st.status === 'available') {
            if (!info.canAutoInstall) {
                if (info.pageUrl) BrowserOpenURL(info.pageUrl);
                return;
            }
            await StartUpdateDownload();
        } else if (st.status === 'downloading') {
            await CancelUpdateDownload();
        } else if (st.status === 'ready') {
            await InstallUpdateAndRestart();
        } else if (st.status === 'error') {
            const retried = await CheckForUpdate(true);
            if (retried.status === 'available' && retried.info?.canAutoInstall) await StartUpdateDownload();
        }
    } catch (err) {
        $('updateHint').textContent = String(err);
    }
});
$('updateSkip').addEventListener('click', async () => {
    const v = updateSnapshot?.info?.version;
    $('updateModal').classList.add('hidden');
    if (!v) return;
    try {
        await SkipUpdateVersion(v);
    } catch (err) {
        appendLog('prefer', 'err', '跳过版本失败：' + err);
    }
});
$('updateLater').addEventListener('click', () => $('updateModal').classList.add('hidden'));
$('closeUpdate').addEventListener('click', () => $('updateModal').classList.add('hidden'));
$('updateModal').querySelector('.bandwidth-modal-backdrop').addEventListener('click', () => $('updateModal').classList.add('hidden'));

$('checkUpdateBtn').addEventListener('click', async () => {
    const btn = $('checkUpdateBtn');
    if (btn.disabled) return;
    btn.disabled = true;
    $('updateSettingsStatus').textContent = '正在检查更新…';
    try {
        const st = await CheckForUpdate(true);
        if (st.status === 'uptodate') {
            $('updateSettingsStatus').textContent = `已是最新版本 v${String(st.currentVersion || '').replace(/^v/i, '')}`;
        } else if (st.status === 'error') {
            $('updateSettingsStatus').textContent = st.error || '检查更新失败';
        }
    } catch (err) {
        $('updateSettingsStatus').textContent = String(err);
    } finally {
        btn.disabled = false;
    }
});
$('setAutoUpdate').addEventListener('change', async (e) => {
    try {
        await SetAutoUpdateCheck(e.target.checked);
    } catch (err) {
        e.target.checked = !e.target.checked;
        appendLog('prefer', 'err', '保存更新设置失败：' + err);
    }
});

async function initUpdater() {
    try {
        const [state, prefs] = await Promise.all([GetUpdateState(), GetUpdatePrefs()]);
        renderUpdateState(state);
        $('setAutoUpdate').checked = !prefs.disableAutoUpdate;
        const current = String(prefs.currentVersion || '').replace(/^v/i, '');
        $('updateSettingsStatus').textContent = current ? `当前版本 v${current}` : '--';
        if (prefs.disableAutoUpdate) return;
        // 每次启动都查一次（不再受上次检查时间限制）；延时 3 秒启动是为了
        // 不和节点拉取、登录恢复抢带宽。跳过该版本的用户由后端静默拦下，不弹提示。
        await CheckForUpdate(false);
    } catch (err) {
        // 自动检查失败保持安静，不影响正常使用。
    }
}
setTimeout(initUpdater, 3000);

// ---------- 启动 ----------
applyTheme();
syncThemeUI();
GetAutostart().then((on) => {
    autostartReady = true;
    // 用户在读取返回前已经操作过开关，就以他的操作为准，不要回填旧值；
    // 这时也别急着解禁，交给 change 处理器的 finally 收尾。
    if (autostartBusy) return;
    autostartEl.checked = on;
    autostartEl.disabled = false;
}).catch((err) => {
    autostartReady = true;
    // 读不到当前状态就不要让用户改：改了也不知道原来是什么、失败也无从回滚
    autostartEl.disabled = true;
    $('autostartHint').textContent = String(err);
});

// 先恢复登录态（过期会自动用 refresh_token 续期），再拉节点。
// 未登录时 /node 仍能拉到节点，但拿不到真实 IP，界面顶部会显示登录引导。
async function bootstrap() {
    // 设置要在首批交互前就绪；失败不阻塞主流程。
    await initSettings();
    let state = null;
    try {
        state = await RestoreSession();
    } catch (err) {
        appendLog('prefer', 'warn', '恢复登录态失败：' + err);
    }
    if (state) {
        qzhuaLoggedIn = Boolean(state.loggedIn);
        $('loginKeep').checked = state.remember !== false;
        if (state.loggedIn && $('accTitle').textContent === '未登录') {
            // 兜底：万一 logged_in 事件早于监听器到达，用快照把侧栏补上
            setText('accTitle', state.username || '轻爪用户');
            setText('accSubtitle', state.email || '已登录');
            $('sideAccount').classList.add('logged-in');
            setAvatar($('accAvatar'), state.picture || '', (state.username || '?').slice(0, 1));
        }
    }
    if (!qzhuaLoggedIn) renderLoginOnly();
    await loadNodes();
}
bootstrap();

// ---------- MC 服务器查询 ----------
// Java 与基岩的 MOTD 统一由后端压平成 § 码文本，这里只负责把它渲染成带样式的行。
const MC_COLORS = {
    '0': '#000000', '1': '#0000aa', '2': '#00aa00', '3': '#00aaaa',
    '4': '#aa0000', '5': '#aa00aa', '6': '#ffaa00', '7': '#aaaaaa',
    '8': '#555555', '9': '#5555ff', 'a': '#55ff55', 'b': '#55ffff',
    'c': '#ff5555', 'd': '#ff55ff', 'e': '#ffff55', 'f': '#ffffff',
};
let mcKind = 'java';
let mcBusy = false;

function renderMotd(box, raw) {
    box.innerHTML = '';
    const text = String(raw || '');
    if (!text.trim()) {
        const empty = document.createElement('div');
        empty.className = 'mc-motd-line';
        empty.textContent = '（服务器没有返回 MOTD）';
        box.appendChild(empty);
        return;
    }
    text.split('\n').forEach((line) => {
        const row = document.createElement('div');
        row.className = 'mc-motd-line';
        const style = { color: '', bold: false, italic: false, underline: false, strike: false, obf: false };
        let buf = '';
        const flush = () => {
            if (!buf) return;
            const span = document.createElement('span');
            span.textContent = buf;
            if (style.color) span.style.color = style.color;
            if (style.bold) span.style.fontWeight = '700';
            if (style.italic) span.style.fontStyle = 'italic';
            if (style.underline) span.style.textDecoration = 'underline';
            if (style.strike) span.style.textDecoration = 'line-through';
            if (style.obf) span.style.opacity = '0.6';
            row.appendChild(span);
            buf = '';
        };
        for (let i = 0; i < line.length; i += 1) {
            const ch = line[i];
            if (ch === '§' && i + 1 < line.length) {
                const code = line[i + 1].toLowerCase();
                flush();
                if (MC_COLORS[code]) {
                    // 颜色码在游戏里会顺带清掉粗体等格式
                    Object.assign(style, { color: MC_COLORS[code], bold: false, italic: false, underline: false, strike: false, obf: false });
                } else if (code === 'l') style.bold = true;
                else if (code === 'o') style.italic = true;
                else if (code === 'n') style.underline = true;
                else if (code === 'm') style.strike = true;
                else if (code === 'k') style.obf = true;
                else if (code === 'r') Object.assign(style, { color: '', bold: false, italic: false, underline: false, strike: false, obf: false });
                i += 1;
                continue;
            }
            buf += ch;
        }
        flush();
        box.appendChild(row);
    });
}

function renderMC(info) {
    const box = $('mcResult');
    box.innerHTML = '';
    const card = document.createElement('div');
    card.className = 'mc-card';
    const head = document.createElement('div');
    head.className = 'mc-card-head';

    const icon = document.createElement('div');
    icon.className = 'mc-icon';
    if (info.favicon) {
        const img = document.createElement('img');
        img.src = info.favicon;
        img.alt = '服务器图标';
        icon.appendChild(img);
    } else {
        icon.textContent = info.kind === 'be' ? 'BE' : 'JE';
    }
    head.appendChild(icon);

    const title = document.createElement('div');
    title.className = 'mc-title';
    const motd = document.createElement('div');
    motd.className = 'mc-motd';
    renderMotd(motd, info.motd);
    title.appendChild(motd);

    const sub = document.createElement('div');
    sub.className = 'mc-sub';
    const tag = document.createElement('span');
    tag.className = info.kind === 'be' ? 'mc-tag be' : 'mc-tag';
    tag.textContent = info.kind === 'be' ? '基岩版' : 'Java 版';
    sub.appendChild(tag);
    const addr = document.createElement('span');
    addr.textContent = `${info.host}:${info.port}`;
    sub.appendChild(addr);
    const extras = [];
    if (info.srv) extras.push('SRV 解析');
    if (info.software) extras.push(info.software);
    if (info.gameMode) extras.push(`模式 ${info.gameMode}`);
    if (info.secureChat) extras.push('强制安全聊天');
    if (extras.length) {
        const tail = document.createElement('span');
        tail.textContent = extras.join(' · ');
        sub.appendChild(tail);
    }
    title.appendChild(sub);
    head.appendChild(title);
    card.appendChild(head);

    const stats = document.createElement('div');
    stats.className = 'mc-stats';
    [
        ['版本', info.version || '未知'],
        ['在线人数', `${info.online} / ${info.max}`],
        ['延迟', `${info.latencyMs} ms`],
        ['协议号', info.protocol ? String(info.protocol) : '未知'],
    ].forEach(([label, value]) => {
        const cell = document.createElement('div');
        cell.className = 'mc-stat';
        const l = document.createElement('span');
        l.textContent = label;
        const v = document.createElement('b');
        v.textContent = value;
        cell.append(l, v);
        stats.appendChild(cell);
    });
    card.appendChild(stats);

    if (info.players && info.players.length) {
        const players = document.createElement('div');
        players.className = 'mc-players';
        const cap = document.createElement('span');
        cap.textContent = `在线玩家示例（${info.players.length}）`;
        const list = document.createElement('div');
        list.className = 'mc-player-list';
        info.players.forEach((name) => {
            const chip = document.createElement('span');
            chip.className = 'mc-player';
            chip.textContent = name;
            list.appendChild(chip);
        });
        players.append(cap, list);
        card.appendChild(players);
    }

    box.appendChild(card);
}

function renderMCError(message) {
    const box = $('mcResult');
    box.innerHTML = '';
    const card = document.createElement('div');
    card.className = 'mc-error';
    const title = document.createElement('b');
    title.textContent = '查询失败';
    const detail = document.createElement('small');
    detail.textContent = message;
    card.append(title, detail);
    box.appendChild(card);
}

async function queryMC() {
    if (mcBusy) return;
    const address = ($('mcAddress').value || '').trim();
    if (!address) {
        $('mcQueryState').textContent = '请先填写地址';
        $('mcAddress').focus();
        return;
    }
    const btn = $('mcQueryBtn');
    mcBusy = true;
    btn.disabled = true;
    btn.textContent = '查询中…';
    $('mcQueryState').textContent = '正在查询…';
    const started = Date.now();
    try {
        const info = await PingMCServer(address, mcKind);
        renderMC(info);
        $('mcQueryState').textContent = `查询完成 · ${Date.now() - started} ms`;
        writeStorage('mcLast', JSON.stringify({ address, kind: mcKind }));
    } catch (err) {
        renderMCError(String(err));
        $('mcQueryState').textContent = '查询失败';
    } finally {
        mcBusy = false;
        btn.disabled = false;
        btn.textContent = '查询';
    }
}

if ($('mcForm')) {
    // 回填上次查询的地址与类型
    const saved = readStorageJson('mcLast', null);
    if (saved && typeof saved === 'object') {
        if (typeof saved.address === 'string' && saved.address) $('mcAddress').value = saved.address;
        if (saved.kind === 'be') mcKind = 'be';
    }
    document.querySelectorAll('#view-mc .mc-kind .filter').forEach((btn) => {
        btn.classList.toggle('active', btn.dataset.mcKind === mcKind);
        btn.addEventListener('click', () => {
            mcKind = btn.dataset.mcKind === 'be' ? 'be' : 'java';
            document.querySelectorAll('#view-mc .mc-kind .filter').forEach((b) => b.classList.toggle('active', b === btn));
            if (($('mcAddress').value || '').trim()) queryMC();
        });
    });
    $('mcForm').addEventListener('submit', (event) => {
        event.preventDefault();
        queryMC();
    });
}
