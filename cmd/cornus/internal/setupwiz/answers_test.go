package setupwiz

import "testing"

func TestBuildContext(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		c := BuildContext(Answers{Scenario: ScenarioLocal, Server: "http://127.0.0.1:5000"})
		if c.Server != "http://127.0.0.1:5000" || c.TLS != nil || c.SSHTunnel != nil || c.PortForward != nil {
			t.Fatalf("local: %+v", c)
		}
	})

	t.Run("url-with-tls-and-token", func(t *testing.T) {
		c := BuildContext(Answers{Server: "https://x", CACert: "/ca.pem", Insecure: true, Token: "tok", RegistryHost: "reg:5000"})
		if c.TLS == nil || c.TLS.CACert != "/ca.pem" || !c.TLS.InsecureSkipVerify {
			t.Fatalf("tls: %+v", c.TLS)
		}
		if c.Token != "tok" || c.RegistryHost != "reg:5000" {
			t.Fatalf("token/registry: %+v", c)
		}
	})

	t.Run("ssh", func(t *testing.T) {
		c := BuildContext(Answers{Scenario: ScenarioSSHDocker, SSHHost: "devbox", SSHUser: "ops", SSHRemoteAddr: "127.0.0.1:5000", SSHTLS: true, ServerName: "cornus.example.com"})
		if c.SSHTunnel == nil || c.SSHTunnel.Addr != "devbox" || c.SSHTunnel.User != "ops" || !c.SSHTunnel.RemoteTLS {
			t.Fatalf("ssh: %+v", c.SSHTunnel)
		}
		if c.TLS == nil || c.TLS.ServerName != "cornus.example.com" {
			t.Fatalf("server-name: %+v", c.TLS)
		}
	})

	t.Run("kube-portforward-with-kubeauth", func(t *testing.T) {
		c := BuildContext(Answers{Scenario: ScenarioKubePortForward, Namespace: "cornus", PFService: "cornus", PFRemotePort: 5000, KubeAuthServiceAccount: "sa", KubeAuthAudience: "aud", KubeAuthNamespace: "cornus"})
		if c.PortForward == nil || c.PortForward.Service != "cornus" || c.PortForward.RemotePort != 5000 {
			t.Fatalf("port-forward: %+v", c.PortForward)
		}
		if c.KubeAuth == nil || c.KubeAuth.ServiceAccount != "sa" || c.KubeAuth.Audience != "aud" {
			t.Fatalf("kube-auth: %+v", c.KubeAuth)
		}
	})
}

func TestSetContextCommandGoldens(t *testing.T) {
	cases := []struct {
		name string
		a    Answers
		want string
	}{
		{
			name: "local",
			a:    Answers{Server: "http://127.0.0.1:5000"},
			want: "cornus config set-context local --server http://127.0.0.1:5000",
		},
		{
			name: "token-redacted",
			a:    Answers{Server: "https://x", Token: "supersecret"},
			want: "cornus config set-context url --server https://x --token REDACTED",
		},
		{
			name: "shell-quoting",
			a:    Answers{Server: "https://x", ServerName: "my host", RegistryHost: "reg host:5000"},
			want: "cornus config set-context q --server https://x --registry-host 'reg host:5000' --tls-server-name 'my host'",
		},
		{
			name: "ssh",
			a:    Answers{SSHHost: "devbox", SSHUser: "ops", SSHRemoteAddr: "127.0.0.1:5000", SSHTLS: true},
			want: "cornus config set-context devbox --ssh-host devbox --ssh-user ops --ssh-remote-addr 127.0.0.1:5000 --ssh-tls",
		},
	}
	names := map[string]string{"local": "local", "token-redacted": "url", "shell-quoting": "q", "ssh": "devbox"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SetContextCommand(names[tc.name], BuildContext(tc.a))
			if got != tc.want {
				t.Errorf("SetContextCommand:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"":              "''",
		"plain":         "plain",
		"http://x:5000": "http://x:5000",
		"has space":     "'has space'",
		"a'b":           `'a'\''b'`,
		"semi;colon":    "'semi;colon'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
