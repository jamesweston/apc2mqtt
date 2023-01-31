{
  description = "Samw's APC PDU MQTT bridge";

  inputs.flake-utils.url = "github:numtide/flake-utils";
  inputs.devshell = {
    url = "github:numtide/devshell";
    inputs.flake-utils.follows = "flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils, devshell }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs {
          inherit system;
          overlays = [ devshell.overlay ];
        };
      in rec {
        packages.apc2mqtt = pkgs.buildGoModule {
          name = "apc2mqtt";
          src = self;
          vendorSha256 = "sha256-GlRcuUNrsNnONigXut2xFx0kX+5k80Djn07c3UutCLU=";
          ldflags = ''
            -X main.version=${if self ? rev then self.rev else "dirty"}
          '';
        };
        defaultPackage = packages.apc2mqtt;
        devShell =
          pkgs.devshell.mkShell { packages = with pkgs; [ go gopls mosquitto ]; };
      });
}
