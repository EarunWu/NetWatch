package main

import "testing"

func TestParseServiceOptionsManagedContract(t *testing.T) {
	options, err := parseServiceOptions([]string{
		"--managed",
		"--listen", "127.0.0.1:9288",
		"--data-dir", `C:\NetWatch Data`,
	}, "unused")
	if err != nil {
		t.Fatal(err)
	}
	if !options.managed || options.listenAddress != listenAddress || options.dataDir != `C:\NetWatch Data` {
		t.Fatalf("unexpected options: %#v", options)
	}
}

func TestParseServiceOptionsRejectsNonLoopbackListener(t *testing.T) {
	for _, address := range []string{"0.0.0.0:9288", ":9288", "example.com:9288", "127.0.0.1:70000"} {
		t.Run(address, func(t *testing.T) {
			if _, err := parseServiceOptions([]string{"--listen", address}, "data"); err == nil {
				t.Fatalf("unsafe or invalid listener accepted: %s", address)
			}
		})
	}
}

func TestParseServiceOptionsKeepsBrowserDefaults(t *testing.T) {
	options, err := parseServiceOptions(nil, "browser-data")
	if err != nil {
		t.Fatal(err)
	}
	if options.managed || options.listenAddress != listenAddress || options.dataDir != "browser-data" {
		t.Fatalf("browser defaults changed: %#v", options)
	}
}
