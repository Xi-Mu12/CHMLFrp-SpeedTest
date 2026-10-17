export namespace main {
	
	export class NodeItem {
	    id: number;
	    name: string;
	    area: string;
	    group: string;
	    web: string;
	    china: string;
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
	        this.udp = source["udp"];
	        this.fangyu = source["fangyu"];
	    }
	}
	export class QZhuaLoginStart {
	    verifyUrl: string;
	
	    static createFrom(source: any = {}) {
	        return new QZhuaLoginStart(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.verifyUrl = source["verifyUrl"];
	    }
	}

}

