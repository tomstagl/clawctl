# clawctl authentication

clawctl never reads a token from an environment variable directly (design
principle #2). Instead it resolves the token from a credential store at
call-time. The resolution order is:

1. `CLAWCTL_TOKEN_CMD` — if set, the command is executed and its stdout is used
2. Platform-specific backend (see below)

## macOS Keychain

Store the gateway token in the macOS Keychain under the service name
`openclaw-gateway-token` (or whatever `CLAWCTL_KEYCHAIN_SERVICE` is set to):

```sh
security add-generic-password \
  -s openclaw-gateway-token \
  -a "$USER" \
  -w
# prompts for the token value
```

To update an existing entry add `-U`:

```sh
security add-generic-password -U \
  -s openclaw-gateway-token \
  -a "$USER" \
  -w
```

## CLAWCTL_TOKEN_CMD (all platforms)

Set `CLAWCTL_TOKEN_CMD` to any shell command whose stdout is the bearer token.
This works on macOS and Linux and overrides the platform backend.

```sh
# read from a file
export CLAWCTL_TOKEN_CMD='cat ~/.config/openclaw/token'

# fetch from 1Password CLI
export CLAWCTL_TOKEN_CMD='op read op://Personal/openclaw/token'

# delegate to a script
export CLAWCTL_TOKEN_CMD='/usr/local/bin/get-openclaw-token'
```

Put the export in your shell rc file (`~/.zshrc`, `~/.bashrc`, etc.) to make
it permanent.

## Linux: secret-tool (libsecret)

On GNOME-based desktops `secret-tool` talks to the system keyring (e.g.
GNOME Keyring or KWallet via the libsecret bridge):

```sh
secret-tool store \
  --label 'openclaw gateway token' \
  service openclaw-gateway-token \
  account "$USER"
# prompts for the token value
```

clawctl will call `secret-tool lookup service openclaw-gateway-token account
$USER` at runtime. Install the tool on Debian/Ubuntu with:

```sh
sudo apt-get install libsecret-tools
```

## Linux: pass (password-store)

`pass` is a GPG-backed password manager. Store the token at the path
`openclaw/gateway-token`:

```sh
pass insert openclaw/gateway-token
# prompts for the token value
```

clawctl will call `pass show openclaw/gateway-token` at runtime.

## Troubleshooting

Run `clawctl init --check` to verify that all three conditions are met:
host is set, token resolves, and the gateway responds with HTTP 200.
