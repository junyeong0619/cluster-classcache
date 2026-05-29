export namespace main {
	
	export class ArchiveSummary {
	    key: string;
	    sizeBytes: number;
	    peerEndpoints: string[];
	    jvm: string;
	    arch: string;
	
	    static createFrom(source: any = {}) {
	        return new ArchiveSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.sizeBytes = source["sizeBytes"];
	        this.peerEndpoints = source["peerEndpoints"];
	        this.jvm = source["jvm"];
	        this.arch = source["arch"];
	    }
	}
	export class ClassCacheSummary {
	    name: string;
	    namespace: string;
	    workloadName: string;
	    profile: string;
	    phase: string;
	    archiveKey: string;
	
	    static createFrom(source: any = {}) {
	        return new ClassCacheSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.namespace = source["namespace"];
	        this.workloadName = source["workloadName"];
	        this.profile = source["profile"];
	        this.phase = source["phase"];
	        this.archiveKey = source["archiveKey"];
	    }
	}
	export class Diag {
	    kubectlOK: boolean;
	    kubectlContext: string;
	    valkeyAddr: string;
	    valkeyReachable: boolean;
	    note: string;
	
	    static createFrom(source: any = {}) {
	        return new Diag(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.kubectlOK = source["kubectlOK"];
	        this.kubectlContext = source["kubectlContext"];
	        this.valkeyAddr = source["valkeyAddr"];
	        this.valkeyReachable = source["valkeyReachable"];
	        this.note = source["note"];
	    }
	}
	export class PodStat {
	    namespace: string;
	    name: string;
	    node: string;
	    cpuMilli: number;
	    memMiB: number;
	
	    static createFrom(source: any = {}) {
	        return new PodStat(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.namespace = source["namespace"];
	        this.name = source["name"];
	        this.node = source["node"];
	        this.cpuMilli = source["cpuMilli"];
	        this.memMiB = source["memMiB"];
	    }
	}
	export class SavingsSnapshot {
	    timestamp: number;
	    totalSizeKiB: number;
	    totalRssKiB: number;
	    totalPssKiB: number;
	    savedKiB: number;
	    sharedCleanKiB: number;
	    sharedDirtyKiB: number;
	    privateCleanKiB: number;
	    privateDirtyKiB: number;
	    jvms: number;
	
	    static createFrom(source: any = {}) {
	        return new SavingsSnapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.timestamp = source["timestamp"];
	        this.totalSizeKiB = source["totalSizeKiB"];
	        this.totalRssKiB = source["totalRssKiB"];
	        this.totalPssKiB = source["totalPssKiB"];
	        this.savedKiB = source["savedKiB"];
	        this.sharedCleanKiB = source["sharedCleanKiB"];
	        this.sharedDirtyKiB = source["sharedDirtyKiB"];
	        this.privateCleanKiB = source["privateCleanKiB"];
	        this.privateDirtyKiB = source["privateDirtyKiB"];
	        this.jvms = source["jvms"];
	    }
	}

}

