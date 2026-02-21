package epub

import "testing"

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
