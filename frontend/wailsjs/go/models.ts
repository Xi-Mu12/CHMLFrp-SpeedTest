export namespace main {
	
	export class AppInfo {
	    version: string;
	    os: string;
	    arch: string;
	
	    static createFrom(source: any = {}) {
	        return new AppInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.os = source["os"];
	        this.arch = source["arch"];
	    }
	}
	export class AppSettings {
	    testSeconds: number;
	    bwParallel: number;
	    bwTunnelConcurrency: number;
	    closeAction?: string;
	    disableAutoUpdate?: boolean;
	    skippedVersion?: string;
	    lastUpdateCheck?: number;
	
	    static createFrom(source: any = {}) {
	        return new AppSettings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.testSeconds = source["testSeconds"];
	        this.bwParallel = source["bwParallel"];
	        this.bwTunnelConcurrency = source["bwTunnelConcurrency"];
	        this.closeAction = source["closeAction"];
	        this.disableAutoUpdate = source["disableAutoUpdate"];
	        this.skippedVersion = source["skippedVersion"];
	        this.lastUpdateCheck = source["lastUpdateCheck"];
	    }
	}
	export class IPGeo {
	    ip: string;
	    version: number;
	    type: string;
	    continent: string;
	    continentCode: string;
	    country: string;
	    countryCode: string;
	    region: string;
	    regionCode: string;
	    city: string;
	    zip: string;
	    latitude: number;
	    longitude: number;
	    isEU: boolean;
	    callingCode: string;
	    capital: string;
	    borders: string;
	    flagEmoji: string;
	    flagImg: string;
	    asn: string;
	    asnNumber: number;
	    isp: string;
	    org: string;
	    domain: string;
	    timeZone: string;
	    timeZoneAbbr: string;
	    utcOffset: string;
	    isDST: boolean;
	
	    static createFrom(source: any = {}) {
	        return new IPGeo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ip = source["ip"];
	        this.version = source["version"];
	        this.type = source["type"];
	        this.continent = source["continent"];
	        this.continentCode = source["continentCode"];
	        this.country = source["country"];
	        this.countryCode = source["countryCode"];
	        this.region = source["region"];
	        this.regionCode = source["regionCode"];
	        this.city = source["city"];
	        this.zip = source["zip"];
	        this.latitude = source["latitude"];
	        this.longitude = source["longitude"];
	        this.isEU = source["isEU"];
	        this.callingCode = source["callingCode"];
	        this.capital = source["capital"];
	        this.borders = source["borders"];
	        this.flagEmoji = source["flagEmoji"];
	        this.flagImg = source["flagImg"];
	        this.asn = source["asn"];
	        this.asnNumber = source["asnNumber"];
	        this.isp = source["isp"];
	        this.org = source["org"];
	        this.domain = source["domain"];
	        this.timeZone = source["timeZone"];
	        this.timeZoneAbbr = source["timeZoneAbbr"];
	        this.utcOffset = source["utcOffset"];
	        this.isDST = source["isDST"];
	    }
	}
	export class LocalIP {
	    iface: string;
	    ip: string;
	    version: number;
	    loopback: boolean;
	    private: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LocalIP(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.iface = source["iface"];
	        this.ip = source["ip"];
	        this.version = source["version"];
	        this.loopback = source["loopback"];
	        this.private = source["private"];
	    }
	}
	export class NodeItem {
	    id: number;
	    name: string;
	    area: string;
	    group: string;
	    web: string;
	    china: string;
	    ip: string;
	    udp: boolean;
	    fangyu: boolean;
	
	    static createFrom(source: any = {}) {
	        return new NodeItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.area = source["area"];
	        this.group = source["group"];
	        this.web = source["web"];
	        this.china = source["china"];
	        this.ip = source["ip"];
	        this.udp = source["udp"];
	        this.fangyu = source["fangyu"];
	    }
	}
	export class QZhuaLoginStart {
	    verifyUrl: string;
	    userCode: string;
	    expiresIn: number;
	
	    static createFrom(source: any = {}) {
	        return new QZhuaLoginStart(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.verifyUrl = source["verifyUrl"];
	        this.userCode = source["userCode"];
	        this.expiresIn = source["expiresIn"];
	    }
	}
	export class SessionState {
	    loggedIn: boolean;
	    remember: boolean;
	    username: string;
	    email: string;
	    picture: string;
	
	    static createFrom(source: any = {}) {
	        return new SessionState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.loggedIn = source["loggedIn"];
	        this.remember = source["remember"];
	        this.username = source["username"];
	        this.email = source["email"];
	        this.picture = source["picture"];
	    }
	}
	export class UpdateInfo {
	    version: string;
	    name: string;
	    notes: string;
	    publishedAt: string;
	    pageUrl: string;
	    size: number;
	    canAutoInstall: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UpdateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.name = source["name"];
	        this.notes = source["notes"];
	        this.publishedAt = source["publishedAt"];
	        this.pageUrl = source["pageUrl"];
	        this.size = source["size"];
	        this.canAutoInstall = source["canAutoInstall"];
	    }
	}
	export class UpdatePrefs {
	    currentVersion: string;
	    disableAutoUpdate: boolean;
	    skippedVersion: string;
	    lastUpdateCheck: number;
	
	    static createFrom(source: any = {}) {
	        return new UpdatePrefs(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.currentVersion = source["currentVersion"];
	        this.disableAutoUpdate = source["disableAutoUpdate"];
	        this.skippedVersion = source["skippedVersion"];
	        this.lastUpdateCheck = source["lastUpdateCheck"];
	    }
	}
	export class UpdateState {
	    status: string;
	    info?: UpdateInfo;
	    downloaded: number;
	    total: number;
	    error?: string;
	    currentVersion: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.info = this.convertValues(source["info"], UpdateInfo);
	        this.downloaded = source["downloaded"];
	        this.total = source["total"];
	        this.error = source["error"];
	        this.currentVersion = source["currentVersion"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

