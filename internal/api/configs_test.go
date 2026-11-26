package api

import (
	"strings"
	"testing"
)

func TestValidateConfigPath(t *testing.T) {
	long := "config/" + strings.Repeat("a", 200) + ".json"
	for _, p := range []string{
		"config/viper_spawn_control.json",
		"config/sub/dir/x.toml",
		"config/a.YAML",
		"config/a.yml",
		"config/server.properties",
		"config/n.txt", "config/n.cfg", "config/n.conf",
	} {
		if err := ValidateConfigPath(p); err != nil {
			t.Errorf("%q sollte gültig sein: %v", p, err)
		}
	}
	for _, p := range []string{
		"",
		"/config/a.json",
		"/etc/passwd",
		"server.properties",
		"config",
		"config/",
		"configs/a.json",
		"Config/a.json",
		"config/../server.properties",
		"config/../../x.json",
		"config/sub/../../x.json",
		"config/./a.json",
		"config//a.json",
		`config\a.json`,
		`config/..\a.json`,
		`C:\config\a.json`,
		"C:/config/a.json",
		"config/C:/a.json",
		`\server\share\config\a.json`,
		"//server/share/a.json",
		"config/a.json:stream",
		"config/a.json::$DATA",
		"config/a.exe",
		"config/a.json.exe",
		"config/a.jar",
		"config/.json",
		"config/a",
		"config/a.json.",
		"config/a.json ",
		"config/nul.json",
		"config/COM1.txt",
		"config/a*.json",
		"config/a?.json",
		"config/a\x00.json",
		"config/a\n.json",
		"config/sub.dir./a.json",
		long,
		"config/" + strings.Repeat("a", 101) + ".json",
	} {
		if err := ValidateConfigPath(p); err == nil {
			t.Errorf("%q hätte abgelehnt werden müssen", p)
		}
	}
}

const (
	h256 = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
)

func TestValidateConfigs(t *testing.T) {
	h512 := "sha512:" + strings.Repeat("ab", 64)
	good := []ConfigFile{
		{Path: "config/a.json", URL: "wanderwelt:a.json", Hash: h256},
		{Path: "config/b.toml", URL: "https://example.org/b.toml", Hash: h512, Mode: "enforce"},
		{Path: "config/c.txt", URL: "WANDERWELT:c.txt", Hash: h256, Mode: "default"},
	}
	if err := ValidateConfigs(good); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfigs(nil); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]ConfigFile{
		"Pfad":          {Path: "../x.json", URL: "wanderwelt:a", Hash: h256},
		"Modus":         {Path: "config/a.json", URL: "wanderwelt:a", Hash: h256, Mode: "immer"},
		"Hash leer":     {Path: "config/a.json", URL: "wanderwelt:a"},
		"Hash kurz":     {Path: "config/a.json", URL: "wanderwelt:a", Hash: "sha256:ab"},
		"Hash md5":      {Path: "config/a.json", URL: "wanderwelt:a", Hash: "md5:" + strings.Repeat("a", 32)},
		"Hash kein hex": {Path: "config/a.json", URL: "wanderwelt:a", Hash: "sha256:" + strings.Repeat("z", 64)},
		"URL http":      {Path: "config/a.json", URL: "http://example.org/a", Hash: h256},
		"URL file":      {Path: "config/a.json", URL: "file:///etc/passwd", Hash: h256},
		"URL leer":      {Path: "config/a.json", Hash: h256},
		"URL Name":      {Path: "config/a.json", URL: "wanderwelt:../a", Hash: h256},
		"URL ohne Host": {Path: "config/a.json", URL: "https:///a", Hash: h256},
	} {
		if err := ValidateConfigs([]ConfigFile{c}); err == nil {
			t.Errorf("%s: hätte abgelehnt werden müssen", name)
		}
	}
	dup := []ConfigFile{
		{Path: "config/a.json", URL: "wanderwelt:a", Hash: h256},
		{Path: "config/A.json", URL: "wanderwelt:b", Hash: h256},
	}
	if err := ValidateConfigs(dup); err == nil || !strings.Contains(err.Error(), "doppelt") {
		t.Errorf("Doppelter Pfad: %v", err)
	}
	many := make([]ConfigFile, MaxConfigs+1)
	if err := ValidateConfigs(many); err == nil {
		t.Error("zu viele Einträge hätten abgelehnt werden müssen")
	}
	if (ConfigFile{}).EffectiveMode() != ConfigEnforce || (ConfigFile{Mode: "default"}).EffectiveMode() != ConfigDefault {
		t.Error("EffectiveMode")
	}
}
