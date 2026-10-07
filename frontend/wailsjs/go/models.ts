export namespace main {
	
	export class Config {
	    host: string;
	    user: string;
	    password: string;
	    labels: string[];
	    saveDir: string;
	    syncMinutes: number;
	    density: string;
	    scale: number;
	    readerZoom: number;
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.host = source["host"];
	        this.user = source["user"];
	        this.password = source["password"];
	        this.labels = source["labels"];
	        this.saveDir = source["saveDir"];
	        this.syncMinutes = source["syncMinutes"];
	        this.density = source["density"];
	        this.scale = source["scale"];
	        this.readerZoom = source["readerZoom"];
	    }
	}
	export class Count {
	    key: string;
	    name: string;
	    total: number;
	    unread: number;
	
	    static createFrom(source: any = {}) {
	        return new Count(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.name = source["name"];
	        this.total = source["total"];
	        this.unread = source["unread"];
	    }
	}
	export class Msg {
	    id: number;
	    fromName: string;
	    fromAddr: string;
	    subject: string;
	    date: number;
	    html?: string;
	    read: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Msg(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.fromName = source["fromName"];
	        this.fromAddr = source["fromAddr"];
	        this.subject = source["subject"];
	        this.date = source["date"];
	        this.html = source["html"];
	        this.read = source["read"];
	    }
	}
	export class Query {
	    label: string;
	    sender: string;
	    search: string;
	    sort: string;
	    offset: number;
	
	    static createFrom(source: any = {}) {
	        return new Query(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.label = source["label"];
	        this.sender = source["sender"];
	        this.search = source["search"];
	        this.sort = source["sort"];
	        this.offset = source["offset"];
	    }
	}

}

