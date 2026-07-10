package main

import rego.v1

instr(cmd) := [i | some i in input; i.Cmd == cmd]

stage_aliases contains alias if {
	some f in instr("from")
	count(f.Value) >= 3
	upper(f.Value[1]) == "AS"
	alias := f.Value[2]
}

is_stage_ref(img) if img in stage_aliases

has_digest(img) if contains(img, "@sha256:")

untagged_or_latest(img) if not contains(img, ":")

untagged_or_latest(img) if endswith(img, ":latest")

deny contains msg if {
	some f in instr("from")
	img := f.Value[0]
	img != "scratch"
	not is_stage_ref(img)
	not has_digest(img)
	untagged_or_latest(img)
	msg := sprintf("FROM %q: pin the base image to an explicit non-latest tag (or @sha256 digest)", [img])
}

deny contains msg if {
	users := instr("user")
	count(users) > 0
	last := lower(users[count(users) - 1].Value[0])
	last in {"root", "0"}
	msg := "final USER must not be root — add a non-root USER before the entrypoint"
}

deny contains msg if {
	some a in instr("add")
	some v in a.Value
	regex.match(`^https?://`, v)
	msg := sprintf("use COPY or a verified download instead of ADD %q", [v])
}

warn contains msg if {
	count(instr("user")) == 0
	msg := "no USER instruction — container runs as root; add a non-root USER (or numeric UID for scratch/distroless)"
}

warn contains msg if {
	count(instr("healthcheck")) == 0
	msg := "no HEALTHCHECK instruction — add one so the orchestrator can detect an unhealthy container"
}

warn contains msg if {
	some f in instr("from")
	img := f.Value[0]
	img != "scratch"
	not is_stage_ref(img)
	not has_digest(img)
	msg := sprintf("FROM %q is pinned by tag, not digest — pin by @sha256:... for reproducible builds", [img])
}

warn contains msg if {
	some r in instr("run")
	val := concat(" ", r.Value)
	regex.match(`(@latest|/latest/download|latest\.tar)`, val)
	msg := "a RUN step fetches a 'latest' artifact — pin the version for reproducible builds"
}

warn contains msg if {
	some r in instr("run")
	val := concat(" ", r.Value)
	contains(val, "apt-get install")
	not contains(val, "--no-install-recommends")
	msg := "apt-get install without --no-install-recommends enlarges image + attack surface"
}

warn contains msg if {
	some e in instr("env")
	some kv in e.Value
	regex.match(`(?i)(password|passwd|secret|api_?key|access_?key|token)`, kv)
	not regex.match(`(?i)^(NODE_ENV|.*_ENV)$`, kv)
	msg := sprintf("ENV token %q may bake a secret into the image — inject secrets at runtime", [kv])
}
