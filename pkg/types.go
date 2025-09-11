package goproc

const (
	GoProcHostPrefix string = "goproc-host"
	GoProcVersion    string = "dev"
)

type GoProcConfig struct {
	ServerPort           uint `key:"serverPort" json:"server_port"`
	GRPCDialTimeoutS     int  `key:"grpcDialTimeoutS" json:"grpc_dial_timeout_s"`
	GRPCMessageSizeBytes int  `key:"grpcMessageSizeBytes" json:"grpc_message_size_bytes"`
	DebugMode            bool `key:"debugMode" json:"debug_mode"`
	PrettyLogs           bool `key:"prettyLogs" json:"pretty_logs"`
}
