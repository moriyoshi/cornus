package cliout

import "testing"

func TestResolveMode(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	none := env(nil)

	cases := []struct {
		name      string
		flag      string
		noColor   bool
		env       func(string) string
		outTTY    bool
		errTTY    bool
		goos      string
		wantMode  Mode
		wantColor bool
	}{
		{"auto both tty -> fancy", "", false, none, true, true, "linux", ModeFancy, true},
		{"auto stdout piped -> plain", "", false, none, false, true, "linux", ModePlain, false},
		{"auto stderr piped -> plain", "", false, none, true, false, "linux", ModePlain, false},
		{"auto no tty -> plain", "", false, none, false, false, "linux", ModePlain, false},
		{"explicit plain wins over tty", "plain", false, none, true, true, "linux", ModePlain, false},
		{"explicit fancy off tty", "fancy", false, none, false, false, "linux", ModeFancy, true},
		{"explicit json", "json", false, none, true, true, "linux", ModeJSON, false},
		{"env fancy via CORNUS_OUTPUT", "", false, env(map[string]string{"CORNUS_OUTPUT": "fancy"}), false, false, "linux", ModeFancy, true},
		{"flag beats env", "plain", false, env(map[string]string{"CORNUS_OUTPUT": "fancy"}), true, true, "linux", ModePlain, false},
		{"no-color flag disables color, keeps fancy", "fancy", true, none, true, true, "linux", ModeFancy, false},
		{"NO_COLOR disables color", "fancy", false, env(map[string]string{"NO_COLOR": "1"}), true, true, "linux", ModeFancy, false},
		{"CLICOLOR=0 disables color", "fancy", false, env(map[string]string{"CLICOLOR": "0"}), true, true, "linux", ModeFancy, false},
		{"windows auto stays plain", "", false, none, true, true, "windows", ModePlain, false},
		{"windows auto fancy when forced", "", false, env(map[string]string{"CLICOLOR_FORCE": "1"}), true, true, "windows", ModeFancy, true},
		{"CLICOLOR_FORCE colors non-tty fancy", "fancy", false, env(map[string]string{"CLICOLOR_FORCE": "1"}), false, false, "linux", ModeFancy, true},
		{"json ignores color forcing", "json", false, env(map[string]string{"CLICOLOR_FORCE": "1"}), true, true, "linux", ModeJSON, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, color := resolveMode(tc.flag, tc.noColor, tc.env, tc.outTTY, tc.errTTY, tc.goos)
			if mode != tc.wantMode || color != tc.wantColor {
				t.Errorf("resolveMode = (%v, %v); want (%v, %v)", mode, color, tc.wantMode, tc.wantColor)
			}
		})
	}
}
