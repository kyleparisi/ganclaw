package api

import "github.com/kyleparisi/ganclaw/internal/config"

// Resolver turns "to"/"via" into a Target using the configured contacts
// and bots.
type Resolver struct {
	Contacts []config.Contact
	Bots     []string
}

func (r Resolver) Resolve(to, via string) (Target, error) {
	if to == "" {
		return Target{}, Errorf(CodeBadRequest, "to is required")
	}
	t, raw := ParseAddress(to)
	if !raw {
		var found bool
		for _, c := range r.Contacts {
			if c.Name == to {
				found = true
				if c.TelegramChat == 0 {
					return Target{}, Errorf(CodeBadRequest, "contact %q has no telegram_chat", to)
				}
				t = Target{Channel: "telegram", Bot: c.DefaultBot, Chat: c.TelegramChat}
			}
		}
		if !found {
			return Target{}, Errorf(CodeUnknownContact, "no contact named %q", to)
		}
	}
	if via != "" {
		t.Bot = via
	}
	if t.Bot == "" {
		return Target{}, Errorf(CodeBadRequest, "contact %q has no default_bot; pass via", to)
	}
	for _, b := range r.Bots {
		if b == t.Bot {
			return t, nil
		}
	}
	return Target{}, Errorf(CodeUnknownBot, "no bot named %q", t.Bot)
}
