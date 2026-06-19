// Port of lume's Server/Requests.swift and Responses.swift: the JSON bodies
// of the HTTP API server.
//go:build darwin

package httpapi

// errorResponse is the uniform error body.
type errorResponse struct {
	Error string `json:"error"`
}

type createVMRequest struct {
	Name       string `json:"name"`
	FromIPSW   string `json:"fromIPSW,omitempty"` // url, path or "latest"; empty means latest for macOS
	Linux      bool   `json:"linux,omitempty"`
	DiskSizeGB uint16 `json:"diskSize,omitempty"`   // default 50
	DiskFormat string `json:"diskFormat,omitempty"` // raw|asif; default raw
	NetProfile string `json:"netProfile,omitempty"` // nat|internet-only|isolated|vm-lab|bridged
}

type cloneVMRequest struct {
	Name        string `json:"name"`
	NewName     string `json:"newName"`
	Registry    string `json:"registry,omitempty"` // registry profile for a remote source
	Insecure    bool   `json:"insecure,omitempty"`
	Concurrency uint   `json:"concurrency,omitempty"` // default 4
	Deduplicate bool   `json:"deduplicate,omitempty"`
	PruneLimit  uint   `json:"pruneLimit,omitempty"` // default 100
}

type updateVMRequest struct {
	CPU          *uint16 `json:"cpu,omitempty"`
	MemoryMB     *uint64 `json:"memory,omitempty"`
	DiskSizeGB   *uint16 `json:"diskSize,omitempty"`
	Display      *string `json:"display,omitempty"`
	DisplayRefit *bool   `json:"displayRefit,omitempty"`
	Disk         *string `json:"disk,omitempty"`
	RandomMAC    bool    `json:"randomMac,omitempty"`
	RandomSerial bool    `json:"randomSerial,omitempty"`
}

// runVMRequest mirrors the full "weave run" flag set; each field maps to a CLI
// flag forwarded to the detached "weave run" subprocess (see handleRunVM).
type runVMRequest struct {
	// Display / graphics. Graphics is caller-controlled: a server inside a GUI
	// login session can open a window. Headless is only the default when
	// neither Graphics nor a VNC mode is requested.
	Graphics        bool `json:"graphics,omitempty"`
	NoGraphics      bool `json:"noGraphics,omitempty"`
	VNC             bool `json:"vnc,omitempty"`
	VNCExperimental bool `json:"vncExperimental,omitempty"`

	VNCPassword string `json:"vncPassword,omitempty"`

	// Serial.
	Serial     bool   `json:"serial,omitempty"`
	SerialPath string `json:"serialPath,omitempty"`

	// Audio.
	NoAudio bool `json:"noAudio,omitempty"`

	// Clipboard + enterprise policy overrides.
	Clipboard             bool   `json:"clipboard,omitempty"`
	NoClipboard           bool   `json:"noClipboard,omitempty"`
	ClipboardUser         string `json:"clipboardUser,omitempty"`
	ClipboardPassword     string `json:"clipboardPassword,omitempty"`
	ClipboardDirection    string `json:"clipboardDirection,omitempty"`
	ClipboardFormats      string `json:"clipboardFormats,omitempty"`
	ClipboardFiles        string `json:"clipboardFiles,omitempty"`
	ClipboardSessionMbps  int    `json:"clipboardSessionMbps,omitempty"`
	ClipboardBandwidthPct int    `json:"clipboardBandwidthPct,omitempty"`
	ClipboardMaxBytes     int64  `json:"clipboardMaxBytes,omitempty"`

	// Storage (repeatable). Mounts is sugar for read-only --disk attachments.
	Disks      []string `json:"disks,omitempty"`
	Dirs       []string `json:"dirs,omitempty"`              // --dir syntax
	SharedDirs []string `json:"sharedDirectories,omitempty"` // --shared-dir syntax
	Mounts     []string `json:"mounts,omitempty"`            // --mount syntax (ISO, read-only)
	USBStorage []string `json:"usbStorage,omitempty"`

	// Networking.
	NetProfile       string   `json:"netProfile,omitempty"`
	NetBridged       []string `json:"netBridged,omitempty"`
	NetDevice        []string `json:"netDevice,omitempty"`
	NetHost          bool     `json:"netHost,omitempty"`
	NetSoftnet       bool     `json:"netSoftnet,omitempty"`
	NetSoftnetAllow  string   `json:"netSoftnetAllow,omitempty"`
	NetSoftnetBlock  string   `json:"netSoftnetBlock,omitempty"`
	NetSoftnetExpose string   `json:"netSoftnetExpose,omitempty"`

	// Misc.
	Rosetta      string `json:"rosetta,omitempty"`
	Nested       bool   `json:"nested,omitempty"`
	Recovery     bool   `json:"recoveryMode,omitempty"`
	Suspendable  bool   `json:"suspendable,omitempty"`
	RootDiskOpts string `json:"rootDiskOpts,omitempty"`

	// Input device toggles (relevant only when a window is shown).
	CaptureSystemKeys bool `json:"captureSystemKeys,omitempty"`
	NoTrackpad        bool `json:"noTrackpad,omitempty"`
	NoPointer         bool `json:"noPointer,omitempty"`
	NoKeyboard        bool `json:"noKeyboard,omitempty"`
}

type renameRequest struct {
	NewName string `json:"newName"`
}

type loginRequest struct {
	Host       string `json:"host"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	Insecure   bool   `json:"insecure,omitempty"`
	NoValidate bool   `json:"noValidate,omitempty"`
}

type importRequest struct {
	Path string `json:"path"` // server-side path to the .tvm archive
	Name string `json:"name"`
}

type execRequest struct {
	Command  []string `json:"command"`
	Resolver string   `json:"resolver,omitempty"`
	Wait     uint16   `json:"wait,omitempty"`
}

type sshRequest struct {
	Command  []string `json:"command,omitempty"` // empty => error over JSON (use the WS endpoint for a shell)
	User     string   `json:"user,omitempty"`
	Password string   `json:"password,omitempty"`
	Timeout  uint64   `json:"timeout,omitempty"`
	Wait     uint16   `json:"wait,omitempty"`
	Resolver string   `json:"resolver,omitempty"`
}

type setupRequest struct {
	Mode          string `json:"mode,omitempty"`       // preset|agent, default preset
	Unattended    string `json:"unattended,omitempty"` // preset name or path (preset mode)
	AnthropicKey  string `json:"anthropicKey,omitempty"`
	Model         string `json:"model,omitempty"`
	MaxIterations int    `json:"maxIterations,omitempty"`
	SystemPrompt  string `json:"systemPrompt,omitempty"`
	ShowScreen    bool   `json:"showScreen,omitempty"`
}

type cacheDirRequest struct {
	CacheDir string `json:"cacheDir"`
}

type ghcrRequest struct {
	Registry     string `json:"registry,omitempty"`
	Organization string `json:"organization,omitempty"`
}

type loggingRequest struct {
	MaxSizeMB   *int  `json:"maxSizeMB,omitempty"`
	KeepRotated *bool `json:"keepRotated,omitempty"`
}

type registryProfileRequest struct {
	Name         string `json:"name"`
	Host         string `json:"host,omitempty"` // default ghcr.io
	Organization string `json:"organization"`
	Insecure     bool   `json:"insecure,omitempty"`
	Default      bool   `json:"default,omitempty"`
}

type execResponse struct {
	ExitCode int32  `json:"exitCode"`
	Output   string `json:"output"` // combined stdout+stderr (exec runs over SSH)
}

type sshResponse struct {
	ExitCode int32  `json:"exitCode"`
	Output   string `json:"output"`
}

type fqnResponse struct {
	FQN string `json:"fqn"`
}

type ipResponse struct {
	IP string `json:"ip"`
}

type stopVMRequest struct {
	Timeout uint64 `json:"timeout,omitempty"` // seconds, default 30
}

type pullVMRequest struct {
	Image       string `json:"image"` // remote name
	Concurrency uint   `json:"concurrency,omitempty"`
	Insecure    bool   `json:"insecure,omitempty"`
	Deduplicate bool   `json:"deduplicate,omitempty"`
}

type pushVMRequest struct {
	Name          string   `json:"name"`
	Images        []string `json:"images"` // remote names
	Registry      string   `json:"registry,omitempty"`
	Concurrency   uint     `json:"concurrency,omitempty"`
	Insecure      bool     `json:"insecure,omitempty"`
	ChunkSize     int      `json:"chunkSize,omitempty"`
	Labels        []string `json:"labels,omitempty"`
	PopulateCache bool     `json:"populateCache,omitempty"`
}

type pruneRequest struct {
	Entries     string `json:"entries,omitempty"` // caches or vms, default caches
	OlderThan   *uint  `json:"olderThan,omitempty"`
	SpaceBudget *uint  `json:"spaceBudget,omitempty"`
	GC          bool   `json:"gc,omitempty"`
}

type storageLocationRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type configUpdateRequest struct {
	DefaultStorage *string `json:"defaultStorage,omitempty"`
	CacheDir       *string `json:"cacheDir,omitempty"`
	Registry       *struct {
		Host         string `json:"host,omitempty"`
		Organization string `json:"organization,omitempty"`
	} `json:"registry,omitempty"`
}

type pullJobResponse struct {
	ID        string `json:"id"`
	Image     string `json:"image"`
	Status    string `json:"status"` // running, succeeded or failed
	Error     string `json:"error,omitempty"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt,omitempty"`
}

type hostStatusResponse struct {
	Version     string `json:"version"`
	Model       string `json:"model,omitempty"`
	CPUCount    int    `json:"cpuCount"`
	MemoryBytes uint64 `json:"memoryBytes"`
}
