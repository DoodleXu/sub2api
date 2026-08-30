export namespace main {
	
	export class ConnectionSummary {
	    configured: boolean;
	    auth_mode?: string;
	    site_url: string;
	    gateway_url: string;
	    label: string;
	    api_key_configured: boolean;
	    api_key_hint: string;
	    api_key_id?: number;
	    codex_api_key_id?: number;
	    claude_api_key_id?: number;
	    session_configured: boolean;
	    device_id?: string;
	    protection_level?: string;
	    scope?: string;
	    updated_at?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConnectionSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.configured = source["configured"];
	        this.auth_mode = source["auth_mode"];
	        this.site_url = source["site_url"];
	        this.gateway_url = source["gateway_url"];
	        this.label = source["label"];
	        this.api_key_configured = source["api_key_configured"];
	        this.api_key_hint = source["api_key_hint"];
	        this.api_key_id = source["api_key_id"];
	        this.codex_api_key_id = source["codex_api_key_id"];
	        this.claude_api_key_id = source["claude_api_key_id"];
	        this.session_configured = source["session_configured"];
	        this.device_id = source["device_id"];
	        this.protection_level = source["protection_level"];
	        this.scope = source["scope"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class APIKeySummary {
	    id: number;
	    name: string;
	    status: string;
	    key_hint: string;
	    quota: number;
	    quota_used: number;
	    expires_at?: string;
	    current_concurrency: number;
	    usage_5h: number;
	    usage_1d: number;
	    usage_7d: number;
	
	    static createFrom(source: any = {}) {
	        return new APIKeySummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.status = source["status"];
	        this.key_hint = source["key_hint"];
	        this.quota = source["quota"];
	        this.quota_used = source["quota_used"];
	        this.expires_at = source["expires_at"];
	        this.current_concurrency = source["current_concurrency"];
	        this.usage_5h = source["usage_5h"];
	        this.usage_1d = source["usage_1d"];
	        this.usage_7d = source["usage_7d"];
	    }
	}
	export class APIKeySelectionResult {
	    selected: APIKeySummary;
	    connection: ConnectionSummary;
	
	    static createFrom(source: any = {}) {
	        return new APIKeySelectionResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.selected = this.convertValues(source["selected"], APIKeySummary);
	        this.connection = this.convertValues(source["connection"], ConnectionSummary);
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
	
	export class AppInfo {
	    name: string;
	    version: string;
	    official_site_url: string;
	
	    static createFrom(source: any = {}) {
	        return new AppInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.version = source["version"];
	        this.official_site_url = source["official_site_url"];
	    }
	}
	export class CheckinResult {
	    reward_amount: number;
	    balance: number;
	    message?: string;
	    checked_in_at?: string;
	
	    static createFrom(source: any = {}) {
	        return new CheckinResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.reward_amount = source["reward_amount"];
	        this.balance = source["balance"];
	        this.message = source["message"];
	        this.checked_in_at = source["checked_in_at"];
	    }
	}
	export class CheckoutSessionInput {
	    amount: number;
	    payment_type: string;
	    order_type?: string;
	    plan_id?: number;
	    upgrade_from_subscription_id?: number;
	
	    static createFrom(source: any = {}) {
	        return new CheckoutSessionInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.amount = source["amount"];
	        this.payment_type = source["payment_type"];
	        this.order_type = source["order_type"];
	        this.plan_id = source["plan_id"];
	        this.upgrade_from_subscription_id = source["upgrade_from_subscription_id"];
	    }
	}
	export class ConnectionInput {
	    site_url: string;
	    gateway_url: string;
	    api_key: string;
	    label: string;
	
	    static createFrom(source: any = {}) {
	        return new ConnectionInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.site_url = source["site_url"];
	        this.gateway_url = source["gateway_url"];
	        this.api_key = source["api_key"];
	        this.label = source["label"];
	    }
	}
	
	export class DeviceAuthorizationInput {
	    device_name: string;
	    scopes?: string[];
	
	    static createFrom(source: any = {}) {
	        return new DeviceAuthorizationInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.device_name = source["device_name"];
	        this.scopes = source["scopes"];
	    }
	}
	export class DeviceAuthorizationStatus {
	    request_id: string;
	    status: string;
	    message?: string;
	    expires_in?: number;
	    device_id?: string;
	    device_name?: string;
	
	    static createFrom(source: any = {}) {
	        return new DeviceAuthorizationStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.request_id = source["request_id"];
	        this.status = source["status"];
	        this.message = source["message"];
	        this.expires_in = source["expires_in"];
	        this.device_id = source["device_id"];
	        this.device_name = source["device_name"];
	    }
	}
	export class DeviceAuthorizationView {
	    request_id: string;
	    user_code: string;
	    verification_url: string;
	    verification_url_complete: string;
	    expires_in: number;
	    interval: number;
	    scope: string;
	    audience: string;
	
	    static createFrom(source: any = {}) {
	        return new DeviceAuthorizationView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.request_id = source["request_id"];
	        this.user_code = source["user_code"];
	        this.verification_url = source["verification_url"];
	        this.verification_url_complete = source["verification_url_complete"];
	        this.expires_in = source["expires_in"];
	        this.interval = source["interval"];
	        this.scope = source["scope"];
	        this.audience = source["audience"];
	    }
	}
	export class DeviceSummary {
	    device_id: string;
	    client_id: string;
	    device_name: string;
	    scopes: string[];
	    audience: string;
	    protection_level: string;
	    created_at: string;
	    last_seen_at: string;
	    revoked_at?: string;
	
	    static createFrom(source: any = {}) {
	        return new DeviceSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.device_id = source["device_id"];
	        this.client_id = source["client_id"];
	        this.device_name = source["device_name"];
	        this.scopes = source["scopes"];
	        this.audience = source["audience"];
	        this.protection_level = source["protection_level"];
	        this.created_at = source["created_at"];
	        this.last_seen_at = source["last_seen_at"];
	        this.revoked_at = source["revoked_at"];
	    }
	}
	export class ImageEditUpload {
	    name: string;
	    content_type?: string;
	    data_url?: string;
	    file_handle?: string;
	    bytes?: number;
	
	    static createFrom(source: any = {}) {
	        return new ImageEditUpload(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.content_type = source["content_type"];
	        this.data_url = source["data_url"];
	        this.file_handle = source["file_handle"];
	        this.bytes = source["bytes"];
	    }
	}
	export class ImageEditInput {
	    model: string;
	    prompt: string;
	    n: number;
	    size: string;
	    quality: string;
	    background: string;
	    output_format: string;
	    output_compression: number;
	    input_fidelity: string;
	    images: ImageEditUpload[];
	    mask?: ImageEditUpload;
	
	    static createFrom(source: any = {}) {
	        return new ImageEditInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.prompt = source["prompt"];
	        this.n = source["n"];
	        this.size = source["size"];
	        this.quality = source["quality"];
	        this.background = source["background"];
	        this.output_format = source["output_format"];
	        this.output_compression = source["output_compression"];
	        this.input_fidelity = source["input_fidelity"];
	        this.images = this.convertValues(source["images"], ImageEditUpload);
	        this.mask = this.convertValues(source["mask"], ImageEditUpload);
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
	
	export class ImageFileHandle {
	    id: string;
	    name: string;
	    content_type: string;
	    bytes: number;
	    expires_at: string;
	
	    static createFrom(source: any = {}) {
	        return new ImageFileHandle(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.content_type = source["content_type"];
	        this.bytes = source["bytes"];
	        this.expires_at = source["expires_at"];
	    }
	}
	export class ImageGenerateInput {
	    model: string;
	    prompt: string;
	    n: number;
	    size: string;
	    quality: string;
	    background: string;
	    output_format: string;
	    output_compression: number;
	
	    static createFrom(source: any = {}) {
	        return new ImageGenerateInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.prompt = source["prompt"];
	        this.n = source["n"];
	        this.size = source["size"];
	        this.quality = source["quality"];
	        this.background = source["background"];
	        this.output_format = source["output_format"];
	        this.output_compression = source["output_compression"];
	    }
	}
	export class ImageHistoryQueryInput {
	    cursor?: string;
	    status?: string;
	    limit?: number;
	
	    static createFrom(source: any = {}) {
	        return new ImageHistoryQueryInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cursor = source["cursor"];
	        this.status = source["status"];
	        this.limit = source["limit"];
	    }
	}
	export class ImageTaskSummary {
	    id: string;
	    task_id: string;
	    api_key_id?: number;
	    status: string;
	    prompt?: string;
	    model?: string;
	    created_at?: string;
	    updated_at?: string;
	
	    static createFrom(source: any = {}) {
	        return new ImageTaskSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.task_id = source["task_id"];
	        this.api_key_id = source["api_key_id"];
	        this.status = source["status"];
	        this.prompt = source["prompt"];
	        this.model = source["model"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class ImageTaskView {
	    id: string;
	    task_id: string;
	    status: string;
	    poll_url?: string;
	    expires_at?: string;
	    assets?: siteclient.ImageAsset[];
	    error?: siteclient.TaskError;
	
	    static createFrom(source: any = {}) {
	        return new ImageTaskView(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.task_id = source["task_id"];
	        this.status = source["status"];
	        this.poll_url = source["poll_url"];
	        this.expires_at = source["expires_at"];
	        this.assets = this.convertValues(source["assets"], siteclient.ImageAsset);
	        this.error = this.convertValues(source["error"], siteclient.TaskError);
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
	export class LocalImageAssetSummary {
	    id: string;
	    name: string;
	    mime_type: string;
	    bytes: number;
	    sha256?: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new LocalImageAssetSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.mime_type = source["mime_type"];
	        this.bytes = source["bytes"];
	        this.sha256 = source["sha256"];
	        this.created_at = source["created_at"];
	    }
	}
	export class ProbeResult {
	    reachable: boolean;
	    site_name?: string;
	    gateway_url?: string;
	    api_base_url?: string;
	    checked_at: string;
	    message?: string;
	
	    static createFrom(source: any = {}) {
	        return new ProbeResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.reachable = source["reachable"];
	        this.site_name = source["site_name"];
	        this.gateway_url = source["gateway_url"];
	        this.api_base_url = source["api_base_url"];
	        this.checked_at = source["checked_at"];
	        this.message = source["message"];
	    }
	}
	export class ToolConfigFile {
	    path: string;
	    backup_path?: string;
	    changed: boolean;
	    contains_secret: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ToolConfigFile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.backup_path = source["backup_path"];
	        this.changed = source["changed"];
	        this.contains_secret = source["contains_secret"];
	    }
	}
	export class ToolConfigInput {
	    tool: string;
	    base_url?: string;
	    model?: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolConfigInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tool = source["tool"];
	        this.base_url = source["base_url"];
	        this.model = source["model"];
	    }
	}
	export class ToolConfigRestoreInput {
	    tool: string;
	    backup_path: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolConfigRestoreInput(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tool = source["tool"];
	        this.backup_path = source["backup_path"];
	    }
	}
	export class ToolConfigRestoreResult {
	    tool: string;
	    target_path: string;
	    previous_backup_path?: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolConfigRestoreResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tool = source["tool"];
	        this.target_path = source["target_path"];
	        this.previous_backup_path = source["previous_backup_path"];
	    }
	}
	export class ToolLaunchPlan {
	    tool: string;
	    environment_variable: string;
	    command: string;
	    shell: string;
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolLaunchPlan(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tool = source["tool"];
	        this.environment_variable = source["environment_variable"];
	        this.command = source["command"];
	        this.shell = source["shell"];
	        this.note = source["note"];
	    }
	}
	export class ToolConfigResult {
	    tool: string;
	    files: ToolConfigFile[];
	    warnings?: string[];
	    launch?: ToolLaunchPlan;
	    completed_at: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolConfigResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tool = source["tool"];
	        this.files = this.convertValues(source["files"], ToolConfigFile);
	        this.warnings = source["warnings"];
	        this.launch = this.convertValues(source["launch"], ToolLaunchPlan);
	        this.completed_at = source["completed_at"];
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
	
	export class ToolLaunchResult {
	    tool: string;
	    executable: string;
	    pid: number;
	    environment_variable: string;
	    started_at: string;
	    message?: string;
	
	    static createFrom(source: any = {}) {
	        return new ToolLaunchResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tool = source["tool"];
	        this.executable = source["executable"];
	        this.pid = source["pid"];
	        this.environment_variable = source["environment_variable"];
	        this.started_at = source["started_at"];
	        this.message = source["message"];
	    }
	}
	export class UsageSummary {
	    mode: string;
	    status?: string;
	    plan_name?: string;
	    remaining: number;
	    balance: number;
	    unit: string;
	    valid: boolean;
	    stats_available: boolean;
	    total_requests?: number;
	    total_tokens?: number;
	    total_cost?: number;
	    total_actual_cost?: number;
	    today_requests?: number;
	    today_tokens?: number;
	    today_cost?: number;
	    today_actual_cost?: number;
	
	    static createFrom(source: any = {}) {
	        return new UsageSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mode = source["mode"];
	        this.status = source["status"];
	        this.plan_name = source["plan_name"];
	        this.remaining = source["remaining"];
	        this.balance = source["balance"];
	        this.unit = source["unit"];
	        this.valid = source["valid"];
	        this.stats_available = source["stats_available"];
	        this.total_requests = source["total_requests"];
	        this.total_tokens = source["total_tokens"];
	        this.total_cost = source["total_cost"];
	        this.total_actual_cost = source["total_actual_cost"];
	        this.today_requests = source["today_requests"];
	        this.today_tokens = source["today_tokens"];
	        this.today_cost = source["today_cost"];
	        this.today_actual_cost = source["today_actual_cost"];
	    }
	}
	export class UsageOverview {
	    account?: UsageSummary;
	    selected_key?: UsageSummary;
	    account_ready: boolean;
	    selected_key_ready: boolean;
	    as_of: string;
	
	    static createFrom(source: any = {}) {
	        return new UsageOverview(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.account = this.convertValues(source["account"], UsageSummary);
	        this.selected_key = this.convertValues(source["selected_key"], UsageSummary);
	        this.account_ready = source["account_ready"];
	        this.selected_key_ready = source["selected_key_ready"];
	        this.as_of = source["as_of"];
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

export namespace siteclient {
	
	export class AsyncImageCapability {
	    enabled: boolean;
	    pollable: boolean;
	    reason?: string;
	
	    static createFrom(source: any = {}) {
	        return new AsyncImageCapability(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.pollable = source["pollable"];
	        this.reason = source["reason"];
	    }
	}
	export class CheckoutSession {
	    session_id: string;
	    status: string;
	    order_id?: number;
	    payment_type?: string;
	    order_type?: string;
	    plan_id?: number;
	    upgrade_from_subscription_id?: number;
	    result_type?: string;
	    amount?: number;
	    pay_amount?: number;
	    currency?: string;
	    browser_url?: string;
	    // Go type: time
	    expires_at: any;
	    // Go type: time
	    created_at: any;
	    poll_after_seconds: number;
	    status_url?: string;
	
	    static createFrom(source: any = {}) {
	        return new CheckoutSession(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.session_id = source["session_id"];
	        this.status = source["status"];
	        this.order_id = source["order_id"];
	        this.payment_type = source["payment_type"];
	        this.order_type = source["order_type"];
	        this.plan_id = source["plan_id"];
	        this.upgrade_from_subscription_id = source["upgrade_from_subscription_id"];
	        this.result_type = source["result_type"];
	        this.amount = source["amount"];
	        this.pay_amount = source["pay_amount"];
	        this.currency = source["currency"];
	        this.browser_url = source["browser_url"];
	        this.expires_at = this.convertValues(source["expires_at"], null);
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.poll_after_seconds = source["poll_after_seconds"];
	        this.status_url = source["status_url"];
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
	export class DeviceFlowCapabilities {
	    grant_type: string;
	    expires_in_seconds: number;
	    poll_interval_seconds: number;
	    pkce_methods: string[];
	    public_key_binding: string;
	    token_type: string;
	    dpop_algorithms: string[];
	    public_key_curves: string[];
	    proof_header: string;
	    nonce_required: boolean;
	    access_token_hash: string;
	
	    static createFrom(source: any = {}) {
	        return new DeviceFlowCapabilities(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.grant_type = source["grant_type"];
	        this.expires_in_seconds = source["expires_in_seconds"];
	        this.poll_interval_seconds = source["poll_interval_seconds"];
	        this.pkce_methods = source["pkce_methods"];
	        this.public_key_binding = source["public_key_binding"];
	        this.token_type = source["token_type"];
	        this.dpop_algorithms = source["dpop_algorithms"];
	        this.public_key_curves = source["public_key_curves"];
	        this.proof_header = source["proof_header"];
	        this.nonce_required = source["nonce_required"];
	        this.access_token_hash = source["access_token_hash"];
	    }
	}
	export class ClientCapabilities {
	    protocol_version: string;
	    server_version?: string;
	    client_id: string;
	    audience: string;
	    api_base_url?: string;
	    scopes: string[];
	    default_scopes: string[];
	    high_risk_scopes: string[];
	    features: Record<string, boolean>;
	    availability: Record<string, string>;
	    backend_mode_enabled: boolean;
	    async_images: AsyncImageCapability;
	    endpoints: Record<string, string>;
	    device_flow: DeviceFlowCapabilities;
	
	    static createFrom(source: any = {}) {
	        return new ClientCapabilities(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.protocol_version = source["protocol_version"];
	        this.server_version = source["server_version"];
	        this.client_id = source["client_id"];
	        this.audience = source["audience"];
	        this.api_base_url = source["api_base_url"];
	        this.scopes = source["scopes"];
	        this.default_scopes = source["default_scopes"];
	        this.high_risk_scopes = source["high_risk_scopes"];
	        this.features = source["features"];
	        this.availability = source["availability"];
	        this.backend_mode_enabled = source["backend_mode_enabled"];
	        this.async_images = this.convertValues(source["async_images"], AsyncImageCapability);
	        this.endpoints = source["endpoints"];
	        this.device_flow = this.convertValues(source["device_flow"], DeviceFlowCapabilities);
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
	
	export class ImageAsset {
	    url: string;
	    revised_prompt?: string;
	
	    static createFrom(source: any = {}) {
	        return new ImageAsset(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.revised_prompt = source["revised_prompt"];
	    }
	}
	export class ImageSecurityCapabilities {
	    allowed_upload_mimes: string[];
	    magic_bytes_required: boolean;
	    decode_dimensions: boolean;
	    https_remote_url_only: boolean;
	    public_remote_url_only: boolean;
	    redirects_validated: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ImageSecurityCapabilities(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.allowed_upload_mimes = source["allowed_upload_mimes"];
	        this.magic_bytes_required = source["magic_bytes_required"];
	        this.decode_dimensions = source["decode_dimensions"];
	        this.https_remote_url_only = source["https_remote_url_only"];
	        this.public_remote_url_only = source["public_remote_url_only"];
	        this.redirects_validated = source["redirects_validated"];
	    }
	}
	export class ImageLimits {
	    max_images: number;
	    max_reference_images: number;
	    max_uploads_with_mask: number;
	    max_upload_part_bytes: number;
	    max_upload_total_bytes: number;
	    max_image_dimension: number;
	    max_image_pixels: number;
	    max_download_bytes: number;
	
	    static createFrom(source: any = {}) {
	        return new ImageLimits(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.max_images = source["max_images"];
	        this.max_reference_images = source["max_reference_images"];
	        this.max_uploads_with_mask = source["max_uploads_with_mask"];
	        this.max_upload_part_bytes = source["max_upload_part_bytes"];
	        this.max_upload_total_bytes = source["max_upload_total_bytes"];
	        this.max_image_dimension = source["max_image_dimension"];
	        this.max_image_pixels = source["max_image_pixels"];
	        this.max_download_bytes = source["max_download_bytes"];
	    }
	}
	export class ImageDefaults {
	    model: string;
	    n: number;
	    size: string;
	    quality: string;
	    output_format: string;
	    background: string;
	    poll_after_seconds: number;
	
	    static createFrom(source: any = {}) {
	        return new ImageDefaults(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model = source["model"];
	        this.n = source["n"];
	        this.size = source["size"];
	        this.quality = source["quality"];
	        this.output_format = source["output_format"];
	        this.background = source["background"];
	        this.poll_after_seconds = source["poll_after_seconds"];
	    }
	}
	export class ImageModelCapability {
	    id: string;
	    operations: string[];
	    enabled: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ImageModelCapability(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.operations = source["operations"];
	        this.enabled = source["enabled"];
	    }
	}
	export class ImageCapabilities {
	    protocol_version: string;
	    endpoint: string;
	    requires_api_key: boolean;
	    operations: string[];
	    models: ImageModelCapability[];
	    defaults: ImageDefaults;
	    limits: ImageLimits;
	    security: ImageSecurityCapabilities;
	    async: AsyncImageCapability;
	    backend_mode_enabled: boolean;
	    server_time: string;
	
	    static createFrom(source: any = {}) {
	        return new ImageCapabilities(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.protocol_version = source["protocol_version"];
	        this.endpoint = source["endpoint"];
	        this.requires_api_key = source["requires_api_key"];
	        this.operations = source["operations"];
	        this.models = this.convertValues(source["models"], ImageModelCapability);
	        this.defaults = this.convertValues(source["defaults"], ImageDefaults);
	        this.limits = this.convertValues(source["limits"], ImageLimits);
	        this.security = this.convertValues(source["security"], ImageSecurityCapabilities);
	        this.async = this.convertValues(source["async"], AsyncImageCapability);
	        this.backend_mode_enabled = source["backend_mode_enabled"];
	        this.server_time = source["server_time"];
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
	
	export class ImageHistoryAsset {
	    task_id: string;
	    asset_index: number;
	    url: string;
	    expires_at: number;
	
	    static createFrom(source: any = {}) {
	        return new ImageHistoryAsset(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.task_id = source["task_id"];
	        this.asset_index = source["asset_index"];
	        this.url = source["url"];
	        this.expires_at = source["expires_at"];
	    }
	}
	export class ImageHistoryItem {
	    id: string;
	    task_id: string;
	    object: string;
	    status: string;
	    http_status?: number;
	    platform?: string;
	    operation?: string;
	    model?: string;
	    image_count?: number;
	    result_count?: number;
	    result_urls?: string[];
	    result?: number[];
	    created_at: number;
	    completed_at?: number;
	    expires_at: number;
	    assets_available: boolean;
	    assets_expired?: boolean;
	    error?: any;
	
	    static createFrom(source: any = {}) {
	        return new ImageHistoryItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.task_id = source["task_id"];
	        this.object = source["object"];
	        this.status = source["status"];
	        this.http_status = source["http_status"];
	        this.platform = source["platform"];
	        this.operation = source["operation"];
	        this.model = source["model"];
	        this.image_count = source["image_count"];
	        this.result_count = source["result_count"];
	        this.result_urls = source["result_urls"];
	        this.result = source["result"];
	        this.created_at = source["created_at"];
	        this.completed_at = source["completed_at"];
	        this.expires_at = source["expires_at"];
	        this.assets_available = source["assets_available"];
	        this.assets_expired = source["assets_expired"];
	        this.error = source["error"];
	    }
	}
	export class ImageHistoryPage {
	    items: ImageHistoryItem[];
	    next_cursor?: string;
	    has_more: boolean;
	    server_time: number;
	
	    static createFrom(source: any = {}) {
	        return new ImageHistoryPage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.items = this.convertValues(source["items"], ImageHistoryItem);
	        this.next_cursor = source["next_cursor"];
	        this.has_more = source["has_more"];
	        this.server_time = source["server_time"];
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
	
	
	
	export class IntegrationProfile {
	    id: string;
	    client_id?: string;
	    audience?: string;
	    auth: string;
	    grant_type?: string;
	    refresh_grant_type?: string;
	    base_path: string;
	    api_key_id?: number;
	    available: boolean;
	    async_capability?: string;
	    endpoints?: string[];
	    configuration?: string[];
	
	    static createFrom(source: any = {}) {
	        return new IntegrationProfile(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.client_id = source["client_id"];
	        this.audience = source["audience"];
	        this.auth = source["auth"];
	        this.grant_type = source["grant_type"];
	        this.refresh_grant_type = source["refresh_grant_type"];
	        this.base_path = source["base_path"];
	        this.api_key_id = source["api_key_id"];
	        this.available = source["available"];
	        this.async_capability = source["async_capability"];
	        this.endpoints = source["endpoints"];
	        this.configuration = source["configuration"];
	    }
	}
	export class IntegrationProfileKey {
	    id: number;
	    name: string;
	    status: string;
	    expires_at?: string;
	    available: boolean;
	
	    static createFrom(source: any = {}) {
	        return new IntegrationProfileKey(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.status = source["status"];
	        this.expires_at = source["expires_at"];
	        this.available = source["available"];
	    }
	}
	export class IntegrationProfileResponse {
	    key_specific: boolean;
	    api_key: IntegrationProfileKey;
	    profiles: IntegrationProfile[];
	
	    static createFrom(source: any = {}) {
	        return new IntegrationProfileResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key_specific = source["key_specific"];
	        this.api_key = this.convertValues(source["api_key"], IntegrationProfileKey);
	        this.profiles = this.convertValues(source["profiles"], IntegrationProfile);
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
	export class TaskError {
	    code?: string;
	    message?: string;
	
	    static createFrom(source: any = {}) {
	        return new TaskError(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.message = source["message"];
	    }
	}

}

