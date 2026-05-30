export namespace bridge {
	
	export class DiagnosticsDTO {
	    proxyRunning: boolean;
	    proxyAddress: string;
	    proxyStartedAt: string;
	    caInstalled: boolean;
	    caCertPath: string;
	    caExpiresAt: string;
	    cursorProxySet: boolean;
	    cursorConfigPath: string;
	    dataDir: string;
	    logDir: string;
	    lastRequestAt: string;
	    lastErrorAt: string;
	    lastErrorMessage: string;
	    totalRequests: number;
	    totalErrors: number;
	
	    static createFrom(source: any = {}) {
	        return new DiagnosticsDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.proxyRunning = source["proxyRunning"];
	        this.proxyAddress = source["proxyAddress"];
	        this.proxyStartedAt = source["proxyStartedAt"];
	        this.caInstalled = source["caInstalled"];
	        this.caCertPath = source["caCertPath"];
	        this.caExpiresAt = source["caExpiresAt"];
	        this.cursorProxySet = source["cursorProxySet"];
	        this.cursorConfigPath = source["cursorConfigPath"];
	        this.dataDir = source["dataDir"];
	        this.logDir = source["logDir"];
	        this.lastRequestAt = source["lastRequestAt"];
	        this.lastErrorAt = source["lastErrorAt"];
	        this.lastErrorMessage = source["lastErrorMessage"];
	        this.totalRequests = source["totalRequests"];
	        this.totalErrors = source["totalErrors"];
	    }
	}
	export class FixFailure {
	    issue: string;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new FixFailure(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.issue = source["issue"];
	        this.error = source["error"];
	    }
	}
	export class FixOptions {
	    fixCATrust: boolean;
	    fixCursorProxy: boolean;
	    clearStatsigCache: boolean;
	    clearAdminCache: boolean;
	    restoreOfficial: boolean;
	
	    static createFrom(source: any = {}) {
	        return new FixOptions(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fixCATrust = source["fixCATrust"];
	        this.fixCursorProxy = source["fixCursorProxy"];
	        this.clearStatsigCache = source["clearStatsigCache"];
	        this.clearAdminCache = source["clearAdminCache"];
	        this.restoreOfficial = source["restoreOfficial"];
	    }
	}
	export class FixedIssue {
	    issue: string;
	    status: string;
	
	    static createFrom(source: any = {}) {
	        return new FixedIssue(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.issue = source["issue"];
	        this.status = source["status"];
	    }
	}
	export class FixResult {
	    success: boolean;
	    fixedIssues: FixedIssue[];
	    failedIssues: FixFailure[];
	    beforeState: DiagnosticsDTO;
	    afterState: DiagnosticsDTO;
	
	    static createFrom(source: any = {}) {
	        return new FixResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.fixedIssues = this.convertValues(source["fixedIssues"], FixedIssue);
	        this.failedIssues = this.convertValues(source["failedIssues"], FixFailure);
	        this.beforeState = this.convertValues(source["beforeState"], DiagnosticsDTO);
	        this.afterState = this.convertValues(source["afterState"], DiagnosticsDTO);
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
	
	export class RuntimeStats {
	    totalRequests: number;
	    byokRequests: number;
	    successfulDialogs: number;
	    failedDialogs: number;
	    failedRequests: number;
	    availableModelPatch: number;
	    cacheHits: number;
	    cacheMisses: number;
	    cacheReadTokens: number;
	    cacheWriteTokens: number;
	    promptTokens: number;
	    completionTokens: number;
	    totalTokens: number;
	    estimatedCost: number;
	    lastModel: string;
	    lastError: string;
	    lastRequest: string;
	
	    static createFrom(source: any = {}) {
	        return new RuntimeStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.totalRequests = source["totalRequests"];
	        this.byokRequests = source["byokRequests"];
	        this.successfulDialogs = source["successfulDialogs"];
	        this.failedDialogs = source["failedDialogs"];
	        this.failedRequests = source["failedRequests"];
	        this.availableModelPatch = source["availableModelPatch"];
	        this.cacheHits = source["cacheHits"];
	        this.cacheMisses = source["cacheMisses"];
	        this.cacheReadTokens = source["cacheReadTokens"];
	        this.cacheWriteTokens = source["cacheWriteTokens"];
	        this.promptTokens = source["promptTokens"];
	        this.completionTokens = source["completionTokens"];
	        this.totalTokens = source["totalTokens"];
	        this.estimatedCost = source["estimatedCost"];
	        this.lastModel = source["lastModel"];
	        this.lastError = source["lastError"];
	        this.lastRequest = source["lastRequest"];
	    }
	}
	export class ProxyState {
	    running: boolean;
	    address: string;
	    baseURL: string;
	    requestCount: number;
	    lastRequest: string;
	    dataDir: string;
	    cursorPath: string;
	    lastError: string;
	    stats: RuntimeStats;
	    trust: certs.TrustStatus;
	
	    static createFrom(source: any = {}) {
	        return new ProxyState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.address = source["address"];
	        this.baseURL = source["baseURL"];
	        this.requestCount = source["requestCount"];
	        this.lastRequest = source["lastRequest"];
	        this.dataDir = source["dataDir"];
	        this.cursorPath = source["cursorPath"];
	        this.lastError = source["lastError"];
	        this.stats = this.convertValues(source["stats"], RuntimeStats);
	        this.trust = this.convertValues(source["trust"], certs.TrustStatus);
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
	export class RequestLogEntry {
	    id: number;
	    time: string;
	    method: string;
	    host: string;
	    path: string;
	    route: string;
	    model: string;
	    statusCode: number;
	    durationMs: number;
	    handled: boolean;
	    byok: boolean;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new RequestLogEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.time = source["time"];
	        this.method = source["method"];
	        this.host = source["host"];
	        this.path = source["path"];
	        this.route = source["route"];
	        this.model = source["model"];
	        this.statusCode = source["statusCode"];
	        this.durationMs = source["durationMs"];
	        this.handled = source["handled"];
	        this.byok = source["byok"];
	        this.error = source["error"];
	    }
	}
	
	export class TrayState {
	    created: boolean;
	    available: boolean;
	    failed: boolean;
	    windowVisible: boolean;
	    proxyRunning: boolean;
	    lastError: string;
	
	    static createFrom(source: any = {}) {
	        return new TrayState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.created = source["created"];
	        this.available = source["available"];
	        this.failed = source["failed"];
	        this.windowVisible = source["windowVisible"];
	        this.proxyRunning = source["proxyRunning"];
	        this.lastError = source["lastError"];
	    }
	}

}

export namespace certs {
	
	export class TrustStatus {
	    trusted: boolean;
	    installed: boolean;
	    store: string;
	    subject: string;
	    thumbprint: string;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new TrustStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.trusted = source["trusted"];
	        this.installed = source["installed"];
	        this.store = source["store"];
	        this.subject = source["subject"];
	        this.thumbprint = source["thumbprint"];
	        this.error = source["error"];
	    }
	}

}

export namespace config {
	
	export class UserConfig {
	    baseURL: string;
	    licenseCode: string;
	    modelAdapters: relay.ModelAdapter[];
	
	    static createFrom(source: any = {}) {
	        return new UserConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.baseURL = source["baseURL"];
	        this.licenseCode = source["licenseCode"];
	        this.modelAdapters = this.convertValues(source["modelAdapters"], relay.ModelAdapter);
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

export namespace relay {
	
	export class ModelAdapter {
	    displayName: string;
	    type: string;
	    baseURL: string;
	    apiKey: string;
	    modelID: string;
	    catalogID?: string;
	    cursorModelID: string;
	    endpoint: string;
	    maxTokens: number;
	    contextWindow: number;
	    temperature: number;
	    inputPricePer1M: number;
	    outputPricePer1M: number;
	    supportsThinking: boolean;
	    supportsImages: boolean;
	    supportsCmdK: boolean;
	    supportsSandboxing: boolean;
	    note?: string;
	    thinkingLevel?: string;
	    extraParamsEnabled?: boolean;
	    extraParamsJSON?: string;
	
	    static createFrom(source: any = {}) {
	        return new ModelAdapter(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.displayName = source["displayName"];
	        this.type = source["type"];
	        this.baseURL = source["baseURL"];
	        this.apiKey = source["apiKey"];
	        this.modelID = source["modelID"];
	        this.catalogID = source["catalogID"];
	        this.cursorModelID = source["cursorModelID"];
	        this.endpoint = source["endpoint"];
	        this.maxTokens = source["maxTokens"];
	        this.contextWindow = source["contextWindow"];
	        this.temperature = source["temperature"];
	        this.inputPricePer1M = source["inputPricePer1M"];
	        this.outputPricePer1M = source["outputPricePer1M"];
	        this.supportsThinking = source["supportsThinking"];
	        this.supportsImages = source["supportsImages"];
	        this.supportsCmdK = source["supportsCmdK"];
	        this.supportsSandboxing = source["supportsSandboxing"];
	        this.note = source["note"];
	        this.thinkingLevel = source["thinkingLevel"];
	        this.extraParamsEnabled = source["extraParamsEnabled"];
	        this.extraParamsJSON = source["extraParamsJSON"];
	    }
	}

}

