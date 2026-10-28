package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

// chromiumMaskError models the Debian wrapper's real binary without executing
// an image. Inspect final runner arguments, including later overlapping mounts.
func chromiumMaskError(args []string) error {
	const directory = "/usr/lib/chromium"
	masked := false
	var override error
	overlaps := func(dst string) bool {
		return dst == directory || strings.HasPrefix(dst, directory+"/") || strings.HasPrefix(directory, strings.TrimSuffix(dst, "/")+"/")
	}
	for i := 0; i < len(args); i++ {
		if args[i] == containerVerifyImage {
			break
		}
		arg, value, attached := strings.Cut(args[i], "=")
		if !attached {
			if i+1 >= len(args) {
				continue
			}
			value = args[i+1]
		}
		switch arg {
		case "--tmpfs":
			dst, options, _ := strings.Cut(value, ":")
			if !overlaps(dst) {
				continue
			}
			flags := strings.Split(options, ",")
			if dst != directory || masked || !slices.Contains(flags, "ro") || slices.Contains(flags, "rw") || !slices.Contains(flags, "noexec") || slices.Contains(flags, "exec") {
				return fmt.Errorf("Chromium binary reachable: unsafe tmpfs %s", value)
			}
			masked = true
		case "--mount":
			for _, field := range strings.Split(value, ",") {
				key, dst, _ := strings.Cut(field, "=")
				if (key == "dst" || key == "destination" || key == "target") && overlaps(dst) {
					return fmt.Errorf("Chromium binary reachable: overlapping mount %s", value)
				}
			}
		case "-v", "--volume":
			fields := strings.Split(value, ":")
			if len(fields) > 1 && overlaps(fields[1]) {
				return fmt.Errorf("Chromium binary reachable: volume %s", value)
			}
		case "-e", "--env":
			key, _, _ := strings.Cut(value, "=")
			if key == "RIBBITTO_REQUIRE_BROWSER" || key == "ROD_BROWSER_BIN" || strings.Contains(key, "CHROME") || key == "PATH" {
				override = fmt.Errorf("browser environment override: %s", key)
			}
		}
	}
	if !masked {
		return fmt.Errorf("Chromium binary /usr/lib/chromium/chromium reachable: mask missing")
	}
	if override != nil {
		return override
	}
	for i, arg := range args {
		if arg == "-c" && i+1 < len(args) {
			script := args[i+1]
			for _, browser := range []string{"chromium", "chrome", "ROD_BROWSER_BIN", "CHROME", "PATH=", "RIBBITTO_REQUIRE_BROWSER"} {
				if strings.Contains(script, browser) {
					return fmt.Errorf("browser script override: %s", browser)
				}
			}
		}
	}
	return nil
}

func TestContainerChromiumMasked(t *testing.T) {
	t.Run("containerArguments-always-masks", func(t *testing.T) {
		if err := chromiumMaskError(containerArguments("copy", "name", "test-browser", "./internal/web", singleContainerReservation())); err != nil {
			t.Fatalf("ordinary argument builder must always mask Chromium: %v", err)
		}
	})
	for _, check := range []string{"build", "vet", "test", "generated", "test-db", "writeback", "test-browser"} {
		t.Run(check, func(t *testing.T) {
			calls := 0
			runner := fakeContainerRunner{func(_ context.Context, args, _ []string, _, _ io.Writer) (int, error) {
				if args[0] == "run" {
					calls++
					err := chromiumMaskError(args)
					if check == "test-browser" {
						if err == nil || !strings.Contains(err.Error(), "mask missing") {
							t.Fatalf("browser Chromium must be reachable without a mask: %v", err)
						}
					} else if err != nil {
						t.Fatalf("non-browser Chromium must be unreachable: %v", err)
					}
				}
				return 0, nil
			}}
			execution := containerExecution{admit: func() string { return "" }, browserRun: browserTestRun, browserImportPath: browserTestPackage}
			_, err := runContainerExecution(context.Background(), containerFixture(t), check, "./internal/web", runner, t.TempDir(), grantContainerExecution(&execution))
			if err != nil || calls != 1 {
				t.Fatalf("runner not reached: calls=%d err=%v", calls, err)
			}
		})
	}
	t.Run("mask-helper", func(t *testing.T) {
		const mask = "/usr/lib/chromium:ro,noexec,nosuid,nodev,size=1m"
		for _, row := range []struct {
			name string
			args []string
			want string
		}{
			{"valid", []string{"--tmpfs", mask}, ""},
			{"missing", nil, "mask missing"},
			{"rw", []string{"--tmpfs", strings.Replace(mask, "ro,", "rw,", 1)}, "unsafe tmpfs"},
			{"no-noexec", []string{"--tmpfs", strings.Replace(mask, "noexec,", "", 1)}, "unsafe tmpfs"},
			{"later-bind", []string{"--tmpfs", mask, "--mount", "type=bind,src=/other,dst=/usr/lib/chromium/chromium,readonly"}, "overlapping mount"},
			{"later-bind-equals", []string{"--tmpfs=" + mask, "--mount=type=bind,src=/other,dst=/usr/lib/chromium/chromium,readonly"}, "overlapping mount"},
			{"later-tmpfs", []string{"--tmpfs", mask, "--tmpfs", "/usr/lib/chromium:rw,exec"}, "unsafe tmpfs"},
			{"volume", []string{"--tmpfs", mask, "-v", "/other:/usr/lib/chromium"}, "volume"},
			{"ancestor-bind", []string{"--tmpfs", mask, "--mount", "type=bind,src=/other,target=/usr/lib"}, "overlapping mount"},
			{"rod-env", []string{"--tmpfs", mask, "-e", "ROD_BROWSER_BIN=/other"}, "environment override"},
			{"chrome-env", []string{"--tmpfs", mask, "-e", "CHROME_BIN=/other"}, "environment override"},
			{"path-env", []string{"--tmpfs", mask, "-e", "PATH=/other"}, "environment override"},
			{"required-env", []string{"--tmpfs", mask, "-e", "RIBBITTO_REQUIRE_BROWSER=1"}, "environment override"},
			{"script", []string{"--tmpfs", mask, containerVerifyImage, "sh", "-c", "exec chromium"}, "script override"},
			{"script-required", []string{"--tmpfs", mask, containerVerifyImage, "sh", "-c", "export RIBBITTO_REQUIRE_BROWSER=1"}, "script override"},
		} {
			t.Run(row.name, func(t *testing.T) {
				err := chromiumMaskError(row.args)
				if row.want == "" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !strings.Contains(err.Error(), row.want) {
					t.Fatalf("mask helper failed to detect %s: %v", row.name, err)
				}
			})
		}
	})
}
