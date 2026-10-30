export namespace main {
	
	export class IPGeo {
	    ip: string;
	    version: number;
	    country: string;
	    countryCode: string;
	    region: string;
	    city: string;
	    zip: string;
	    isp: string;
	    org: string;
	    as: string;
	    timeZone: string;
	    latitude: number;
	    longitude: number;
	
	    static createFrom(source: any = {}) {
	        return new IPGeo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ip = source["ip"];
	        this.version = source["version"];
	        this.country = source["country"];
	        this.countryCode = source["countryCode"];
	        this.region = source["region"];
	        this.city = source["city"];
	        this.zip = source["zip"];
	        this.isp = source["isp"];
	        this.org = source["org"];
	        this.as = source["as"];
	        this.timeZone = source["timeZone"];
	        this.latitude = source["latitude"];
	        this.longitude = source["longitude"];
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

}

