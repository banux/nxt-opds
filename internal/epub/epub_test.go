package epub

import (
	"testing"
)

func TestFindFirstImgSrc(t *testing.T) {
	cases := []struct {
		name string
		html string
		want string
	}{
		{
			name: "double-quoted src",
			html: `<html><body><img src="images/cover.jpg" alt="cover"/></body></html>`,
			want: "images/cover.jpg",
		},
		{
			name: "single-quoted src",
			html: `<img src='../Images/cover.png'>`,
			want: "../Images/cover.png",
		},
		{
			name: "unquoted src",
			html: `<img src=cover.jpg>`,
			want: "cover.jpg",
		},
		{
			name: "src with query string stripped",
			html: `<img src="cover.jpg?v=1">`,
			want: "cover.jpg",
		},
		{
			name: "src with fragment stripped",
			html: `<img src="cover.jpg#top">`,
			want: "cover.jpg",
		},
		{
			name: "uppercase IMG tag",
			html: `<IMG SRC="cover.jpg">`,
			want: "cover.jpg",
		},
		{
			name: "no img tag",
			html: `<html><body><p>No image here</p></body></html>`,
			want: "",
		},
		{
			name: "img without src",
			html: `<img alt="cover">`,
			want: "",
		},
		{
			name: "first img wins",
			html: `<img src="first.jpg"><img src="second.jpg">`,
			want: "first.jpg",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := findFirstImgSrc(tc.html)
			if got != tc.want {
				t.Errorf("findFirstImgSrc(%q) = %q, want %q", tc.html, got, tc.want)
			}
		})
	}
}

func TestExtractSeriesFromMetas(t *testing.T) {
	cases := []struct {
		name      string
		metas     []opfMeta
		wantName  string
		wantIndex string
	}{
		{
			name:      "empty metas",
			metas:     nil,
			wantName:  "",
			wantIndex: "",
		},
		{
			name: "calibre epub2 style",
			metas: []opfMeta{
				{Name: "calibre:series", Content: "The Expanse"},
				{Name: "calibre:series_index", Content: "1"},
			},
			wantName:  "The Expanse",
			wantIndex: "1",
		},
		{
			name: "calibre epub2 case insensitive",
			metas: []opfMeta{
				{Name: "Calibre:Series", Content: "Dune"},
				{Name: "CALIBRE:SERIES_INDEX", Content: "2"},
			},
			wantName:  "Dune",
			wantIndex: "2",
		},
		{
			name: "calibre series without index",
			metas: []opfMeta{
				{Name: "calibre:series", Content: "Foundation"},
			},
			wantName:  "Foundation",
			wantIndex: "",
		},
		{
			name: "epub3 belongs-to-collection with refines",
			metas: []opfMeta{
				{Property: "belongs-to-collection", ID: "s1", Value: "Hyperion Cantos"},
				{Property: "collection-type", Refines: "#s1", Value: "series"},
				{Property: "group-position", Refines: "#s1", Value: "1"},
			},
			wantName:  "Hyperion Cantos",
			wantIndex: "1",
		},
		{
			name: "epub3 no collection-type defaults to series",
			metas: []opfMeta{
				{Property: "belongs-to-collection", ID: "c1", Value: "Culture"},
				{Property: "group-position", Refines: "#c1", Value: "2"},
			},
			wantName:  "Culture",
			wantIndex: "2",
		},
		{
			name: "epub3 set collection-type skipped",
			metas: []opfMeta{
				{Property: "belongs-to-collection", ID: "c1", Value: "Anthology"},
				{Property: "collection-type", Refines: "#c1", Value: "set"},
			},
			wantName:  "",
			wantIndex: "",
		},
		{
			name: "calibre takes precedence over epub3",
			metas: []opfMeta{
				{Name: "calibre:series", Content: "Calibre Series"},
				{Property: "belongs-to-collection", ID: "s1", Value: "EPUB3 Series"},
			},
			wantName:  "Calibre Series",
			wantIndex: "",
		},
		{
			name: "irrelevant metas ignored",
			metas: []opfMeta{
				{Name: "cover", Content: "cover-image"},
				{Property: "dcterms:modified", Value: "2023-01-01T00:00:00Z"},
			},
			wantName:  "",
			wantIndex: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotIndex := extractSeriesFromMetas(tc.metas)
			if gotName != tc.wantName {
				t.Errorf("series name = %q, want %q", gotName, tc.wantName)
			}
			if gotIndex != tc.wantIndex {
				t.Errorf("series index = %q, want %q", gotIndex, tc.wantIndex)
			}
		})
	}
}
