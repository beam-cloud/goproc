package goproc

import "testing"

func TestLoadGoProcConfigUsesFastDefault(t *testing.T) {
	t.Setenv("CONFIG_PATH", "")
	t.Setenv("CONFIG_JSON", "")

	cfg, err := LoadGoProcConfig()
	if err != nil {
		t.Fatalf("LoadGoProcConfig returned error: %v", err)
	}
	if cfg != DefaultGoProcConfig() {
		t.Fatalf("LoadGoProcConfig = %+v, want %+v", cfg, DefaultGoProcConfig())
	}
}

func TestDefaultGoProcConfigMatchesEmbeddedYAML(t *testing.T) {
	t.Setenv("CONFIG_PATH", "")
	t.Setenv("CONFIG_JSON", "")

	configManager, err := NewConfigManager[GoProcConfig]()
	if err != nil {
		t.Fatalf("NewConfigManager returned error: %v", err)
	}
	if cfg := configManager.GetConfig(); cfg != DefaultGoProcConfig() {
		t.Fatalf("embedded default config = %+v, want %+v", cfg, DefaultGoProcConfig())
	}
}

func TestLoadGoProcConfigKeepsConfigJSONOverride(t *testing.T) {
	t.Setenv("CONFIG_PATH", "")
	t.Setenv("CONFIG_JSON", `{"server_port":7222,"pretty_logs":false}`)

	cfg, err := LoadGoProcConfig()
	if err != nil {
		t.Fatalf("LoadGoProcConfig returned error: %v", err)
	}
	if cfg.ServerPort != 7222 {
		t.Fatalf("ServerPort = %d, want 7222", cfg.ServerPort)
	}
	if cfg.PrettyLogs {
		t.Fatal("PrettyLogs = true, want false")
	}
}

func TestLoadGoProcConfigReadsSocketFromEnvironment(t *testing.T) {
	for name, configJSON := range map[string]string{
		"default":     "",
		"config json": `{"server_port":7222,"server_socket":"/run/other.sock"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CONFIG_PATH", "")
			t.Setenv("CONFIG_JSON", configJSON)
			t.Setenv("GOPROC_SOCKET", "/run/manager.sock")

			cfg, err := LoadGoProcConfig()
			if err != nil {
				t.Fatalf("LoadGoProcConfig returned error: %v", err)
			}
			if cfg.ServerSocket != "/run/manager.sock" {
				t.Fatalf("ServerSocket = %q, want /run/manager.sock", cfg.ServerSocket)
			}
		})
	}
}
