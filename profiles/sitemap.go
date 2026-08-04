package profiles

import (
	"encoding/xml"
	"net/http"
	"strings"

	"backend/configs"
	"backend/users"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	XMLNS   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

// HandleArtisanSitemap generates an XML sitemap containing all public artisan
// profiles. Each entry links to the canonical server-rendered page.
func HandleArtisanSitemap(ctx echo.Context) error {
	filter := bson.M{
		"is_profile_public": true,
		"user_type":         "artisan",
		"slug":              bson.M{"$ne": ""},
	}

	requestCtx := ctx.Request().Context()
	cursor, err := configs.MI.DB.Collection("users").Find(
		requestCtx,
		filter,
		options.Find().SetProjection(bson.M{"slug": 1, "updated_at": 1}),
	)
	if err != nil {
		return ctx.XML(http.StatusInternalServerError, nil)
	}
	defer cursor.Close(requestCtx)

	var artisans []users.User
	if err := cursor.All(requestCtx, &artisans); err != nil {
		return ctx.XML(http.StatusInternalServerError, nil)
	}

	baseURL := strings.TrimRight(configs.APIPublicURL(), "/")
	urlSet := sitemapURLSet{
		XMLNS: "http://www.sitemaps.org/schemas/sitemap/0.9",
		URLs:  make([]sitemapURL, 0, len(artisans)),
	}
	for _, a := range artisans {
		lastMod := ""
		if !a.UpdatedAt.IsZero() {
			lastMod = a.UpdatedAt.Format("2006-01-02")
		}
		urlSet.URLs = append(urlSet.URLs, sitemapURL{
			Loc:     baseURL + "/artisan/" + a.Slug,
			LastMod: lastMod,
		})
	}

	ctx.Response().Header().Set("Content-Type", "application/xml; charset=utf-8")
	ctx.Response().Header().Set("Cache-Control", "public, s-maxage=3600, max-age=300")
	ctx.Response().WriteHeader(http.StatusOK)
	_, _ = ctx.Response().Write([]byte(xml.Header))
	enc := xml.NewEncoder(ctx.Response())
	enc.Indent("", "  ")
	return enc.Encode(urlSet)
}
