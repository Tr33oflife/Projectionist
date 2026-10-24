package meta

// ai_collections_test.go — v1.21: ProposeCollections parsing. The
// contract is deliberately stricter than the other AI plug points:
// a malformed reply is an ERROR (the Advisor run fails plainly),
// while a well-formed empty array is a valid "nothing to suggest".

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestProposeCollectionsCleanArray(t *testing.T) {
	_, c := aiServer(t, func(w http.ResponseWriter, r *http.Request) {
		chatOK(w, `[{"title":"Cozy Horror","summary":"Comfort scares","rationale":"Shared autumn mood","memberRatingKeys":["11","12","13","14","15"]}]`)
	})
	props, err := c.ProposeCollections(context.Background(), "batch")
	if err != nil {
		t.Fatalf("ProposeCollections: %v", err)
	}
	if len(props) != 1 || props[0].Title != "Cozy Horror" || len(props[0].MemberRatingKeys) != 5 {
		t.Fatalf("proposals = %+v", props)
	}
}

func TestProposeCollectionsFencedProseReply(t *testing.T) {
	_, c := aiServer(t, func(w http.ResponseWriter, r *http.Request) {
		chatOK(w, "Here are my picks:\n```json\n[{\"title\":\"A\",\"summary\":\"s\",\"rationale\":\"r\",\"memberRatingKeys\":[\"1\",\"2\",\"3\",\"4\",\"5\"]}]\n```\nHope that helps!")
	})
	props, err := c.ProposeCollections(context.Background(), "batch")
	if err != nil {
		t.Fatalf("ProposeCollections fenced: %v", err)
	}
	if len(props) != 1 || props[0].Title != "A" {
		t.Fatalf("proposals = %+v", props)
	}
}

func TestProposeCollectionsNumericKeysCoerced(t *testing.T) {
	_, c := aiServer(t, func(w http.ResponseWriter, r *http.Request) {
		chatOK(w, `[{"title":"Nums","summary":"s","rationale":"r","memberRatingKeys":[101,102,103,104,105]}]`)
	})
	props, err := c.ProposeCollections(context.Background(), "batch")
	if err != nil {
		t.Fatalf("ProposeCollections numeric keys: %v", err)
	}
	if len(props) != 1 || props[0].MemberRatingKeys[0] != "101" || props[0].MemberRatingKeys[4] != "105" {
		t.Fatalf("keys = %+v", props[0].MemberRatingKeys)
	}
}

func TestProposeCollectionsEmptyArrayIsValid(t *testing.T) {
	_, c := aiServer(t, func(w http.ResponseWriter, r *http.Request) {
		chatOK(w, `[]`)
	})
	props, err := c.ProposeCollections(context.Background(), "batch")
	if err != nil {
		t.Fatalf("empty array must not error: %v", err)
	}
	if len(props) != 0 {
		t.Fatalf("proposals = %+v, want none", props)
	}
}

func TestProposeCollectionsMalformedIsAnError(t *testing.T) {
	cases := map[string]string{
		"plain prose":      "I cannot suggest collections for this batch.",
		"object not array": `{"title":"Nope","memberRatingKeys":["1","2","3","4","5"]}`,
		"broken json":      `[{"title":"x",`,
		"empty reply":      "",
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			_, c := aiServer(t, func(w http.ResponseWriter, r *http.Request) {
				chatOK(w, reply)
			})
			props, err := c.ProposeCollections(context.Background(), "batch")
			if err == nil {
				t.Fatalf("reply %q: want an error, got proposals %+v", reply, props)
			}
			if !strings.Contains(err.Error(), "AI") {
				t.Fatalf("error should read as an AI failure, got: %v", err)
			}
		})
	}
}
