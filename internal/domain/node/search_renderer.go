package node

// SearchRendererVersion identifies the text that [EmitSearchText] produces
// for every stored input. Any change to that text requires a larger value.
// Each organization records the renderer version its indexed pages use.
// A writer indexes an organization only when the recorded renderer equals
// this value and the active projection format matches the writer's format.
// Version 0 identifies the text of releases that did not record a version.
const SearchRendererVersion = 1
