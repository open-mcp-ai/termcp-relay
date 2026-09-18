//go:build linux

package service

import kservice "github.com/kardianos/service"

// systemdScript is the built-in systemd template with two changes:
//   - RestartSec=5 instead of the library's hardcoded 120, so a crash (for
//     example a port that is briefly still in TIME_WAIT) is retried promptly
//     rather than after two minutes.
//   - After=network-online.target, so the unit starts once the network is up.
//
// Everything else is copied verbatim; the template engine rejects unknown keys,
// so the set of referenced keys must match what the library provides.
const systemdScript = `[Unit]
Description={{Description}}
After=network-online.target
Wants=network-online.target
ConditionFileIsExecutable={{Path | cmdEscape}}
{{range Dependencies}}{{.}}
{{end}}
[Service]
StartLimitInterval=5
StartLimitBurst=10
ExecStart={{Path | cmdEscape}}{{range Arguments}} {{. | cmd}}{{end}}
{{if ChRoot}}RootDirectory={{ChRoot | cmd}}
{{end}}{{if WorkingDirectory}}WorkingDirectory={{WorkingDirectory | cmdEscape}}
{{end}}{{if UserName}}User={{UserName}}
{{end}}{{if ReloadSignal}}ExecReload=/bin/kill -{{ReloadSignal}} "$MAINPID"
{{end}}{{if PIDFile}}PIDFile={{PIDFile | cmd}}
{{end}}{{if OutputFileSupport}}StandardOutput=file:{{LogDirectory}}/{{Name}}.out
StandardError=file:{{LogDirectory}}/{{Name}}.err
{{end}}{{if LimitNOFILE}}LimitNOFILE={{LimitNOFILE}}
{{end}}{{if Restart}}Restart={{Restart}}
{{end}}{{if SuccessExitStatus}}SuccessExitStatus={{SuccessExitStatus}}
{{end}}RestartSec=5
EnvironmentFile=-/etc/sysconfig/{{Name}}

{{range EnvVars}}{{.}}
{{end}}[Install]
WantedBy=multi-user.target
`

// applyPlatformOptions tunes the Linux service definition. The custom systemd
// template above is ignored by SysV/Upstart/OpenRC backends, which do not
// support restart policies at all. "SystemdScript" mirrors the library's
// unexported optionSystemdScript key.
func applyPlatformOptions(o kservice.KeyValue) {
	o["SystemdScript"] = systemdScript
}
