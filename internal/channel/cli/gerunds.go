package cli

import (
	"math/rand/v2"
	"strings"
)

// gerunds are the playful action verbs the spinner line cycles through, one
// per turn. The list is curated from the Tupi-Guarani dictionary in
// https://biblioteca.funai.gov.br/ ; each gerund is the English translation
// of the meaning, conjugated with -ing.
//
//	Aracê   = dawn / morning birdsong          -> Dawning
//	Aracema = a flock of birds                 -> Flocking
//	Anhana  = pushed, propelled                -> Propelling
//	Apoena  = the one who sees far             -> Farseeing
//	Apuama  = swift, that has current          -> Coursing
//	Arapuca = a bird trap of stacked sticks    -> Trapping
//	Aracy   = mother / origin of the day       -> Originating
//	Eçabara = the campaigner, the scout        -> Scouting
//	Goitacá = nomad, wandering, never settles  -> Wandering
//	Iracema = lips of honey                    -> Honeying
//	Javari  = ceremonial competition           -> Sparring
//	Maracá  = ritual rattle / shaker           -> Rattling
//	Moponga = beating water to drive fish      -> Splashing
//	Mutirão = communal gathering / collective  -> Gathering
//	Nhenhenhém = chatter, talking a lot        -> Chattering
//	Puçanga = home remedy                      -> Remedying
//	Tiririca = the weed that spreads fast      -> Spreading
//
// Three classic Claude-Code-style gerunds are kept for familiarity.
var gerunds = []string{
	"Shimmying",
	"Pondering",
	"Crunching",
	"Dawning",
	"Flocking",
	"Propelling",
	"Farseeing",
	"Coursing",
	"Trapping",
	"Originating",
	"Scouting",
	"Wandering",
	"Honeying",
	"Sparring",
	"Rattling",
	"Splashing",
	"Gathering",
	"Chattering",
	"Remedying",
	"Spreading",
}

// pickGerund returns a random gerund. Centralised so tests can stub it later
// if determinism is wanted.
func pickGerund() string {
	if len(gerunds) == 0 {
		return "Working"
	}
	return gerunds[rand.IntN(len(gerunds))]
}

// estimateTokens is the same rough char/4 estimator memory.EstimateTokens uses
// — duplicated here to avoid a cross-package import for what is otherwise a
// trivial helper. Used for the live ↓ tokens counter on the spinner line.
func estimateTokens(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return len(s)/4 + 1
}
