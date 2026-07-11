package nix

// Template is a starting-point workload expression offered at creation
// time. Its shape is type-specific: for a nixos-container the content is
// the full containers.<name> attrset value, so everything (networking,
// mounts, autoStart) stays in the user's hands. Each WorkloadType carries
// its own template set.
type Template struct {
	ID          string
	Name        string
	Description string
	Content     string
	Ports       []HostPort // host firewall ports to open by default
}

// containerTemplates are offered when creating a nixos-container; the
// nixos-container WorkloadType references this slice.
var containerTemplates = []Template{
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
		Description: "nginx on the host network; nixbox opens port 8080 on the host firewall.",
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

    system.stateVersion = "26.05";
  };
}
`,
		// Shared host namespace: the port opens in the host firewall, which
		// nixbox manages via the Host ports field rather than inside config.
		Ports: []HostPort{{Port: 8080, Proto: "tcp"}},
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
	{
		ID:   "flake-module",
		Name: "Flake module",
		Description: "Runs a module from a flake declared in the Flakes tab, " +
			"inside the container. Note the leading `{ flakeInputs }:`.",
		// The whole expression is a function of { flakeInputs } (the inputs
		// declared in the Flakes tab). It imports one input's NixOS module
		// into the container and configures it. Swap "example" for the input
		// name you added, and set its options.
		Content: `{ flakeInputs }: {
  autoStart = true;

  config = { config, pkgs, lib, ... }: {
    # Requires a flake input named "example" (Flakes tab). The module runs
    # inside this container, so its ports are the container's — open them in
    # the Host ports field if the container shares the host network.
    imports = [ flakeInputs.example.nixosModules.default ];

    # Configure whatever the module provides, e.g.:
    # services.example.enable = true;

    system.stateVersion = "26.05";
  };
}
`,
	},
}

// ociTemplates are offered when creating an oci-container; the
// oci-container WorkloadType references this slice. The content is the
// value of virtualisation.oci-containers.containers.<name> — a podman
// container spec — so image/ports/volumes/environment stay in the user's
// hands. Images are fully qualified because podman does not assume a
// default registry.
var ociTemplates = []Template{
	{
		ID:          "blank",
		Name:        "Blank",
		Description: "A single image kept running by a long-lived command.",
		Content: `{
  image = "docker.io/library/alpine:latest";
  cmd = [ "sh" "-c" "while true; do sleep 3600; done" ];

  # environment = { TZ = "UTC"; };
  # volumes = [ "mydata:/data" ];
}
`,
	},
	{
		ID:          "nginx",
		Name:        "Web server",
		Description: "nginx image publishing :8080 on the host via podman.",
		Content: `{
  image = "docker.io/library/nginx:stable";
  # podman publishes host 8080 -> container 80 and installs its own
  # firewall rules for the mapping, so no Host ports entry is needed. (A
  # host-networked container — network_mode = "host" — would drop this
  # mapping and instead need 8080 in Host ports, like a nixos-container.)
  ports = [ "8080:80" ];
}
`,
	},
}

// TemplateByID resolves one of this type's templates by ID.
func (wt WorkloadType) TemplateByID(id string) (Template, bool) {
	for _, t := range wt.Templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}
