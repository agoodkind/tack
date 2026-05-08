package tools

import "strings"

const referenceParamSuffix = "_reference"

func (r *Resolver) entryPointReference(args argMap) (string, bool) {
	return requireString(args, r.EntryPointParamName())
}

func (r *Resolver) optionalEntryPointReference(args argMap) string {
	return optionalString(args, r.EntryPointParamName())
}

func (r *Resolver) entryPointRequiredMessage() string {
	return r.EntryPointParamName() + " is required"
}

func entryPointSchemaFields(resolver *Resolver) []schemaField {
	return []schemaField{{
		Name: resolver.EntryPointParamName(),
		Type: schemaString,
		Desc: entryPointFieldDescription(resolver),
		Enum: nil,
	}}
}

func scopeReferenceFields(level ScopeLevel, resolver *Resolver) []schemaField {
	return []schemaField{{Name: level.ParamName, Type: schemaString, Desc: scopeFieldDescription(level, resolver), Enum: nil}}
}

func requireScopeReference(args argMap, level ScopeLevel) (string, bool) {
	return requireString(args, level.ParamName)
}

func optionalScopeReference(args argMap, level ScopeLevel) string {
	return optionalString(args, level.ParamName)
}

func scopeReferenceRequiredMessage(level ScopeLevel) string {
	return level.ParamName + " is required"
}

func scopeParamNames(chain []ScopeLevel) []string {
	names := make([]string, 0, len(chain))
	for _, level := range chain {
		names = append(names, level.ParamName)
	}
	return names
}

func typeReferenceParamName(nodeTypeToken string) string {
	return strings.ToLower(nodeTypeToken) + referenceParamSuffix
}
