package cli

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cmd     *command
		args    []string
		env     map[string]string
		want    options
		wantErr string
	}{
		{cmd: loginCmd, args: []string{"--email", "a@b.c"}, want: &loginOptions{email: "a@b.c"}},
		{cmd: loginCmd, env: map[string]string{"YODEA_EMAIL": "env@b.c"}, want: &loginOptions{email: "env@b.c"}},
		{cmd: loginCmd, args: []string{"-email=flag@b.c"}, env: map[string]string{"YODEA_EMAIL": "env@b.c"}, want: &loginOptions{email: "flag@b.c"}},
		{cmd: loginCmd, wantErr: "login needs --email (or $YODEA_EMAIL)"},
		{cmd: loginCmd, args: []string{"--email", "a@b.c", "extra"}, wantErr: `usage: yodea login --email E (login takes options only, got unexpected "extra")`},
		{cmd: loginCmd, args: []string{"--password", "x"}, wantErr: "flag provided but not defined: -password"},

		{cmd: initCmd, want: &initOptions{dir: "."}},
		{cmd: initCmd, args: []string{"--dir", "d", "--force", "--link", "demo"}, want: &initOptions{dir: "d", force: true, link: true, project: "demo"}},
		{cmd: initCmd, args: []string{"demo", "--dir", "x"}, wantErr: `flag "--dir" must come before the project name; usage: yodea init [--dir D] [--force] [--link] [project]`},
		{cmd: initCmd, args: []string{"a", "b"}, wantErr: "init takes at most one project name (got 2); put flags before the project name; usage: yodea init [--dir D] [--force] [--link] [project]"},

		{cmd: pushCmd, want: &pushOptions{dir: "."}},
		{cmd: pushCmd, args: []string{"--dir", "d", "--project", "p"}, want: &pushOptions{dir: "d", project: "p"}},
		{cmd: pushCmd, args: []string{"--dir", "d", "--project", "p", "--static"}, want: &pushOptions{dir: "d", project: "p", static: true}},
		{cmd: pushCmd, args: []string{"extra"}, wantErr: `usage: yodea push [--dir D] [--project P] [--static] (got unexpected argument "extra"; push uploads an already built dist/ and does not build it)`},

		{cmd: listCmd, want: &listOptions{}},
		{cmd: listCmd, args: []string{"extra"}, wantErr: `usage: yodea list (got unexpected argument "extra")`},
		{cmd: listCmd, args: []string{"-h"}, wantErr: "flag: help requested"},

		{cmd: deleteCmd, args: []string{"myapp"}, want: &deleteOptions{project: "myapp"}},
		{cmd: deleteCmd, wantErr: "usage: yodea delete <project> (takes exactly one project name)"},
		{cmd: deleteCmd, args: []string{"my", "app"}, wantErr: "usage: yodea delete <project> (takes exactly one project name)"},
	}
	for _, tc := range cases {
		a, _, _ := newTestApp(t, tc.env)
		got, err := a.parse(tc.cmd, tc.args)
		if tc.wantErr != "" {
			if err == nil || err.Error() != tc.wantErr {
				t.Errorf("%s %q: error = %v, want %q", tc.cmd.name, tc.args, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s %q: error = %v", tc.cmd.name, tc.args, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s %q: options = %+v, want %+v", tc.cmd.name, tc.args, got, tc.want)
		}
	}
}
