package orchestrator

import (
	"fmt"
	"log/slog"
	"strconv"

	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/registry"
)

// AgentKindDeprecations returns one advisory per distinct agent kind
// cfg reaches for which metaOf reports the kind registered with a
// non-nil [registry.AgentMeta.Deprecation], in the order
// [orderedUniqueAgentKinds] yields. It performs no I/O and no
// mutation, and returns nil when no reachable kind is deprecated.
func AgentKindDeprecations(cfg config.ServiceConfig, metaOf func(kind string) (registry.AgentMeta, bool)) []config.Advisory {
	var advisories []config.Advisory
	for _, ref := range orderedUniqueAgentKinds(cfg) {
		meta, registered := metaOf(ref.Kind)
		if !registered || meta.Deprecation == nil {
			continue
		}
		advisories = append(advisories, config.Advisory{
			Check: "agent.kind.deprecated",
			Text: fmt.Sprintf("agent kind %s is deprecated and will be removed in a later release; use agent kind %s instead",
				strconv.Quote(ref.Kind), strconv.Quote(meta.Deprecation.Replacement)),
			Message: "agent kind is deprecated and will be removed in a later release",
			Attrs: []slog.Attr{
				slog.String("agent_kind", ref.Kind),
				slog.String("replacement_kind", meta.Deprecation.Replacement),
			},
		})
	}
	return advisories
}
