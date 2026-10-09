export namespace main {
	
	export class Attachment {
	    name: string;
	    type: string;
	    size: number;
	
	    static createFrom(source: any = {}) {
	        return new Attachment(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.type = source["type"];
	        this.size = source["size"];
	    }
	}
	export class Config {
	    host: string;
	    user: string;
	    password: string;
	    labels: string[];
	    saveDir: string;
	    syncMinutes: number;
	    density: string;
	    layout: string;
	    scale: number;
	    readerZoom: number;
	    allImages: boolean;
	    imageSenders: string[];
	
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
	        this.layout = source["layout"];
	        this.scale = source["scale"];
	        this.readerZoom = source["readerZoom"];
	        this.allImages = source["allImages"];
	        this.imageSenders = source["imageSenders"];
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
	export class Folder {
	    name: string;
	    role: string;
	    total: number;
	    unread: number;
	    synced: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Folder(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.role = source["role"];
	        this.total = source["total"];
	        this.unread = source["unread"];
	        this.synced = source["synced"];
	    }
	}
	export class Moved {
	    id: number;
	    from: string;
	    to: string;
	    uid: number;
	
	    static createFrom(source: any = {}) {
	        return new Moved(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.from = source["from"];
	        this.to = source["to"];
	        this.uid = source["uid"];
	    }
	}
	export class Msg {
	    id: number;
	    fromName: string;
	    fromAddr: string;
	    to: string;
	    subject: string;
	    date: number;
	    read: boolean;
	    flagged: boolean;
	    html?: string;
	    labels?: string[];
	    attachments?: Attachment[];
	    blocked?: number;
	
	    static createFrom(source: any = {}) {
	        return new Msg(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.fromName = source["fromName"];
	        this.fromAddr = source["fromAddr"];
	        this.to = source["to"];
	        this.subject = source["subject"];
	        this.date = source["date"];
	        this.read = source["read"];
	        this.flagged = source["flagged"];
	        this.html = source["html"];
	        this.labels = source["labels"];
	        this.attachments = this.convertValues(source["attachments"], Attachment);
	        this.blocked = source["blocked"];
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
	export class ServerFolder {
	    name: string;
	    default: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ServerFolder(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.default = source["default"];
	    }
	}
	export class SyncStatus {
	    running: boolean;
	    label: string;
	    count: number;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new SyncStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.label = source["label"];
	        this.count = source["count"];
	        this.error = source["error"];
	    }
	}

}

