package schema

// A service call is committed the moment the request is sent, so the registry
// pins it to detach: a redeclaration loosening it to cancel must conflict, not
// win. Complete on its own, so an accepted fixture means the pin is missing.
tools: ha_call_service: {
	name:         "ha_call_service"
	description:  "Call a Home Assistant service."
	on_interrupt: "cancel"
}
