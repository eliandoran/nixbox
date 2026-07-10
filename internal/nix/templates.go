package nix

// Template is a starting-point workload expression offered at creation
// time. The content is the full containers.<name> attrset value, so
// everything (networking, mounts, autoStart) stays in the user's hands.
type Template struct {
	ID          string
	Name        string
	Description string
	Content     string
}

var Templates = []Template{
	{
		ID:          "blank",
		Name:        "Blank",
		Description: "Minimal skeleton sharing the host network.",
		Content: `{
  autoStart = true;

  config = { config, pkgs, lib, ... }: {

    system.stateVersion = "26.05";
  };
}
`,
	},
	{
		ID:          "nginx",
		Name:        "Web server",
		Description: "nginx on the host network, port 8080.",
		Content: `{
  autoStart = true;

  config = { config, pkgs, lib, ... }: {
    services.nginx = {
      enable = true;
      virtualHosts.localhost = {
        listen = [ { addr = "0.0.0.0"; port = 8080; } ];
        root = pkgs.writeTextDir "index.html" "<h1>Hello from nixbox</h1>";
      };
    };
    networking.firewall.allowedTCPPorts = [ 8080 ];

    system.stateVersion = "26.05";
  };
}
`,
	},
	{
		ID:          "private-net",
		Name:        "Private network",
		Description: "Own network namespace with static host/container addresses.",
		Content: `{
  autoStart = true;
  privateNetwork = true;
  hostAddress = "10.100.0.1";
  localAddress = "10.100.0.2";

  config = { config, pkgs, lib, ... }: {
    services.nginx.enable = true;
    networking.firewall.allowedTCPPorts = [ 80 ];

    system.stateVersion = "26.05";
  };
}
`,
	},
}

func TemplateByID(id string) (Template, bool) {
	for _, t := range Templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}
