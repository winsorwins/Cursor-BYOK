// Bridge service for calling Wails v2 backend methods

// Mock implementation for development
const mockBridge = {
  mockStats() {
    return {
      totalRequests: 0,
      byokRequests: 0,
      successfulDialogs: 0,
      failedDialogs: 0,
      failedRequests: 0,
      availableModelPatch: 0,
      cacheHits: 0,
      cacheMisses: 0,
      promptTokens: 0,
      completionTokens: 0,
      totalTokens: 0,
      estimatedCost: 0,
      lastModel: '',
      lastError: '',
      lastRequest: ''
    }
  },

  mockTrust() {
    return {
      trusted: false,
      installed: false,
      store: 'CurrentUser\\Root',
      subject: '',
      thumbprint: '',
      error: ''
    }
  },

  async startProxy() {
    console.log('[Bridge] startProxy called')
    return {
      running: true,
      address: '127.0.0.1:18080',
      baseURL: 'https://api2.cursor.sh',
      stats: this.mockStats(),
      trust: this.mockTrust()
    }
  },

  async stopProxy() {
    console.log('[Bridge] stopProxy called')
    return {
      running: false,
      address: '127.0.0.1:18080',
      baseURL: 'https://api2.cursor.sh',
      stats: this.mockStats(),
      trust: this.mockTrust()
    }
  },

  async getState() {
    console.log('[Bridge] getState called')
    return {
      running: false,
      address: '127.0.0.1:18080',
      baseURL: 'https://api2.cursor.sh',
      stats: this.mockStats(),
      trust: this.mockTrust()
    }
  },

  async setBaseURL(url) {
    console.log('[Bridge] setBaseURL called:', url)
    return {
      running: false,
      address: '127.0.0.1:18080',
      baseURL: url,
      stats: this.mockStats(),
      trust: this.mockTrust()
    }
  },

  async loadUserConfig() {
    console.log('[Bridge] loadUserConfig called')
    return {
      baseURL: 'https://api2.cursor.sh',
      licenseCode: '',
      modelAdapters: []
    }
  },

  async saveUserConfig(config) {
    console.log('[Bridge] saveUserConfig called:', config)
  },

  async getCACertificate() {
    console.log('[Bridge] getCACertificate called')
    return '-----BEGIN CERTIFICATE-----\nMOCK CERTIFICATE\n-----END CERTIFICATE-----'
  },

  async ensureCACertificateTrusted() {
    console.log('[Bridge] ensureCACertificateTrusted called')
    return { ...this.mockTrust(), trusted: true, installed: true }
  },

  async getCACertificateTrustStatus() {
    console.log('[Bridge] getCACertificateTrustStatus called')
    return this.mockTrust()
  },

  async addModelAdapter(adapter) {
    console.log('[Bridge] addModelAdapter called:', adapter)
  },

  async testModelAdapter(adapter) {
    console.log('[Bridge] testModelAdapter called:', adapter)
    return '测试通过'
  },

  async removeModelAdapter(modelID) {
    console.log('[Bridge] removeModelAdapter called:', modelID)
  },

  async listModelAdapters() {
    console.log('[Bridge] listModelAdapters called')
    return []
  },

  async listRequestLogs() {
    console.log('[Bridge] listRequestLogs called')
    return []
  },

  async clearRequestLogs() {
    console.log('[Bridge] clearRequestLogs called')
  },

  async restoreCursorSettings() {
    console.log('[Bridge] restoreCursorSettings called')
    return this.getState()
  },

  async getDiagnostics() {
    console.log('[Bridge] getDiagnostics called')
    return {
      proxyRunning: false,
      proxyAddress: '127.0.0.1:18080',
      proxyStartedAt: '',
      caInstalled: false,
      caCertPath: '',
      caExpiresAt: '',
      cursorProxySet: false,
      cursorConfigPath: '',
      dataDir: '',
      logDir: '',
      lastRequestAt: '',
      lastErrorAt: '',
      lastErrorMessage: '',
      totalRequests: 0,
      totalErrors: 0
    }
  },

  async fixIssues(options) {
    console.log('[Bridge] fixIssues called:', options)
    return {
      success: true,
      fixedIssues: [],
      failedIssues: [],
      beforeState: await this.getDiagnostics(),
      afterState: await this.getDiagnostics()
    }
  }
}

// Try to use real Wails v2 bindings, fallback to mock
let bridge = mockBridge

try {
  // Wails v2 bindings are available at window.go.main.ProxyService
  if (window.go && window.go.bridge && window.go.bridge.ProxyService) {
    const ProxyService = window.go.bridge.ProxyService

    bridge = {
      startProxy: () => ProxyService.StartProxy(),
      stopProxy: () => ProxyService.StopProxy(),
      getState: () => ProxyService.GetState(),
      setBaseURL: (url) => ProxyService.SetBaseURL(url),
      loadUserConfig: () => ProxyService.LoadUserConfig(),
      saveUserConfig: (config) => ProxyService.SaveUserConfig(config),
      getCACertificate: () => ProxyService.GetCACertificate(),
      ensureCACertificateTrusted: () => ProxyService.EnsureCACertificateTrusted(),
      getCACertificateTrustStatus: () => ProxyService.GetCACertificateTrustStatus(),
      addModelAdapter: (adapter) => ProxyService.AddModelAdapter(adapter),
      testModelAdapter: (adapter) => ProxyService.TestModelAdapter(adapter),
      removeModelAdapter: (modelID) => ProxyService.RemoveModelAdapter(modelID),
      listModelAdapters: () => ProxyService.ListModelAdapters(),
      listRequestLogs: () => ProxyService.ListRequestLogs(),
      clearRequestLogs: () => ProxyService.ClearRequestLogs(),
      restoreCursorSettings: () => ProxyService.RestoreCursorSettings(),
      getDiagnostics: () => ProxyService.GetDiagnostics(),
      fixIssues: (options) => ProxyService.FixIssues(options)
    }

    console.log('[Bridge] Using Wails v2 bindings')
  }
} catch (err) {
  console.warn('[Bridge] Using mock bridge, Wails bindings not available:', err)
}

export { bridge }
