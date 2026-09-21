package main

import (
	"reflect"
	"testing"

	pluginv1 "github.com/Bloem-Studios/bloem-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestExistingGenresFieldAndMerge(t *testing.T) {
	req := &pluginv1.GetMetadataRequest{}
	raw := protowire.AppendTag(nil, existingGenresField, protowire.BytesType)
	raw = protowire.AppendString(raw, "Comedy")
	raw = protowire.AppendTag(raw, 99, protowire.VarintType)
	raw = protowire.AppendVarint(raw, 42)
	raw = protowire.AppendTag(raw, existingGenresField, protowire.BytesType)
	raw = protowire.AppendString(raw, "Custom Tag")
	req.ProtoReflect().SetUnknown(raw)

	got := mergeGenres(existingGenresFromRequest(req), []string{"Action", "comedy", "Drama"})
	want := []string{"Action", "Comedy", "Custom Tag", "Drama"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged genres = %#v, want %#v", got, want)
	}
}
