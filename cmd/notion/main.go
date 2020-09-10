package main

import (
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"

	_ "github.com/joho/godotenv/autoload" // load .env

	"github.com/kjk/notionapi"
	"github.com/kjk/notionapi/tomarkdown"
	"golang.org/x/sync/errgroup"
)

func main() {
	client := &notionapi.Client{}
	client.AuthToken = os.Getenv("NOTION_TOKEN")

	colID := os.Getenv("BLOG_COLLECTION_ID")
	index, err := queryCollection(client, colID, os.Getenv("BLOG_COLLECTION_VIEW_ID"))
	if err != nil {
		log.Fatalln("failed to query blog index", err)
	}

	g := New(10)
	total := len(index.RecordMap.Blocks)
	var done int64
	for k, v := range index.RecordMap.Blocks {
		if v == nil {
			total--
			continue
		}
		if k == colID {
			total--
			continue
		}
		if v.Block.ParentID != colID {
			total--
			continue
		}
		if v.Block.Type != "page" {
			total--
			log.Println("not a page:", k, v.Block.Type)
			continue
		}

		k := k
		g.Go(func() error {
			return renderPage(
				client,
				k,
				func(t string) {
					log.Println("[", atomic.AddInt64(&done, 1), "/", total, "]", t)
				},
				func(page *notionapi.Page) string {
					return toString(page.Root().Prop("properties.S6_\""))
				},
				func(page *notionapi.Page) string {
					slug := toString(page.Root().Prop("properties.S6_\""))
					return fmt.Sprintf("content/post/%s.md", strings.ReplaceAll(slug, "/", ""))
				},
				func(page *notionapi.Page) string {
					slug := toString(page.Root().Prop("properties.S6_\""))
					date := toDateString(page.Root().Prop("properties.a`af"))
					draft := !toBool(page.Root().Prop("properties.la`A"))
					city := toString(page.Root().Prop("properties.%]Hm"))
					title := page.Root().Title
					return blogHeader(title, date, draft, slug, city)
				},
				func(page *notionapi.Page) bool {
					return !toBool(page.Root().Prop("properties.la`A"))
				},
				func(page *notionapi.Page) error {
					if toString(page.Root().Prop("properties.S6_\"")) == "" {
						return errors.New("missing slug")
					}
					if toDateString(page.Root().Prop("properties.a`af")) == "" {
						return errors.New("missing date")
					}
					if page.Root().Title == "" {
						return errors.New("title")
					}

					return nil
				},
			)
		})
	}

	if err := g.Wait(); err != nil {
		log.Fatalln(err)
	}

	colID = os.Getenv("OTHER_COLLECTION_ID")
	index, err = queryCollection(client, colID, os.Getenv("OTHER_COLLECTION_VIEW_ID"))
	if err != nil {
		log.Fatalln("failed to query other pages index", err)
	}

	total = len(index.RecordMap.Blocks)
	done = 0
	for k, v := range index.RecordMap.Blocks {
		if v == nil {
			total--
			continue
		}
		if k == colID {
			total--
			continue
		}
		if v.Block.ParentID != colID {
			total--
			continue
		}
		if v.Block.Type != "page" {
			total--
			log.Println("not a page:", k, v.Block.Type)
			continue
		}

		k := k
		g.Go(func() error {
			return renderPage(
				client,
				k,
				func(t string) {
					log.Println("[", atomic.AddInt64(&done, 1), "/", total, "]", t)
				},
				func(page *notionapi.Page) string {
					return toString(page.Root().Prop("properties.7F2|"))
				},
				func(page *notionapi.Page) string {
					slug := toString(page.Root().Prop("properties.7F2|"))
					return fmt.Sprintf("content/%s.md", strings.ReplaceAll(slug, "/", ""))
				},
				func(page *notionapi.Page) string {
					return pageHeader(page.Root().Title)
				},
				func(page *notionapi.Page) bool {
					return false
				},
				func(page *notionapi.Page) error {
					if toString(page.Root().Prop("properties.7F2|")) == "" {
						return errors.New("missing slug")
					}
					if page.Root().Title == "" {
						return errors.New("title")
					}

					return nil
				},
			)
		})
	}

	if err := g.Wait(); err != nil {
		log.Fatalln(err)
	}
}

func queryCollection(client *notionapi.Client, colID, colViewID string) (*notionapi.QueryCollectionResponse, error) {
	log.Println("Querying collection", colID)
	return client.QueryCollection(colID, colViewID, &notionapi.Query{
		Aggregate: []*notionapi.AggregateQuery{
			{
				AggregationType: "count",
				ID:              "count",
				Type:            "title",
				Property:        "title",
				ViewType:        "table",
			},
		},
		FilterOperator: "and",
		Sort: []*notionapi.QuerySort{
			{
				Direction: "descending",
				Property:  "a`af",
			},
		},
	}, &notionapi.User{
		Locale:   "en",
		TimeZone: "America/Sao_Paulo",
	})
}

var tweetExp = regexp.MustCompile(`^https://twitter.com/.*/status/(\d+).*$`)

func renderPage(
	client *notionapi.Client,
	k string,
	logger func(t string),
	slugProvider func(p *notionapi.Page) string,
	filenameProvider func(p *notionapi.Page) string,
	headerProvider func(p *notionapi.Page) string,
	pageSkipper func(p *notionapi.Page) bool,
	pageValidator func(p *notionapi.Page) error,
) error {
	page, err := client.DownloadPage(k)
	if err != nil {
		return err
	}

	if pageSkipper(page) {
		logger("skipping " + page.Root().Title)
		return nil
	}

	if err := pageValidator(page); err != nil {
		return err
	}

	slug := slugProvider(page)

	logger("rendering " + slug)

	converter := tomarkdown.NewConverter(page)
	converter.RenderBlockOverride = func(block *notionapi.Block) bool {
		if block.Type == notionapi.BlockCode {
			converter.Printf("```" + toLang(block.CodeLanguage) + "\n")
			converter.Printf(block.Code + "\n")
			converter.Printf("```\n")
			return true
		}

		if block.Type == notionapi.BlockTweet {
			converter.Newline()
			converter.Printf("{{< tweet %s >}}", tweetExp.FindStringSubmatch(block.Source)[1])
			converter.Newline()
			return true
		}

		if block.Type == notionapi.BlockImage {
			file, err := client.DownloadFile(block.Source, block.ID)
			if err != nil {
				log.Fatalln(err)
			}
			imgPath := fmt.Sprintf("static/public/images/%s/%s%s", slug, block.ID, path.Ext(block.Source))
			log.Println("downloading image", imgPath)
			if err := os.MkdirAll(filepath.Dir(imgPath), 0750); err != nil {
				log.Fatalln(err)
			}
			if err := ioutil.WriteFile(imgPath, file.Data, 0644); err != nil {
				log.Fatalln(err)
			}
			converter.Printf(
				"![%s](%s)\n",
				toCaption(block),
				strings.Replace(imgPath, "static/", "/", 1),
			)
			return true
		}

		return false
	}

	if err := ioutil.WriteFile(
		filenameProvider(page),
		buildMarkdown(
			headerProvider(page),
			converter.ToMarkdown(),
		),
		0644,
	); err != nil {
		return err
	}
	return nil
}

func toCaption(block *notionapi.Block) string {
	if block.GetCaption() == nil {
		return ""
	}

	var caption = ""
	for _, t := range block.GetCaption() {
		caption += t.Text
	}
	return caption
}

func toLang(s string) string {
	if s == "Plain Text" {
		return ""
	}
	return strings.ToLower(s)
}

var postURLRegex = regexp.MustCompile(`\(https://carlosbecker.com/posts/(.+)/\)`)

func buildMarkdown(header string, content []byte) []byte {
	ss := strings.Replace(string(content), "---", "<!--more-->", 1) // replaces the first divider with the more thing for hugo
	ss = strings.NewReplacer(
		"“", "\"",
		"”", "\"",
		"’", "'",
		"‘", "'",
		"…", "...",
	).Replace(ss)

	ss = postURLRegex.ReplaceAllString(ss, "({{< ref $1.md >}})")

	return []byte(
		strings.Join(
			append(
				[]string{header},
				strings.Split(ss, "\n")[1:]...,
			),
			"\n",
		) + "\n",
	)
}

func blogHeader(title, date string, draft bool, slug, city string) string {
	return fmt.Sprintf(`---
title: "%s"
date: %s
draft: %v
slug: %s
city: %s
---`, title, date, draft, slug, city)
}

func pageHeader(title string) string {
	return fmt.Sprintf(`---
title: "%s"
type: page
---`, title)
}

func toBool(v interface{}, ok bool) bool {
	return toString(v, ok) == "Yes"
}

func toString(v interface{}, ok bool) string {
	if !ok {
		return ""
	}

	return v.([]interface{})[0].([]interface{})[0].(string)
}

func toDateString(v interface{}, ok bool) string {
	if !ok {
		return ""
	}

	// may god have mercy on my soul
	return v.([]interface{})[0].([]interface{})[1].([]interface{})[0].([]interface{})[1].(map[string]interface{})["start_date"].(string)
}

//
// copied from goreleaser codebase
//

// Group is the Semphore ErrorGroup itself.
type Group interface {
	Go(func() error)
	Wait() error
}

// New returns a new Group of a given size.
func New(size int) Group {
	return &parallelGroup{
		ch: make(chan bool, size),
		g:  errgroup.Group{},
	}
}

var _ Group = &parallelGroup{}

type parallelGroup struct {
	ch chan bool
	g  errgroup.Group
}

// Go execs one function respecting the group and semaphore.
func (s *parallelGroup) Go(fn func() error) {
	s.g.Go(func() error {
		s.ch <- true
		defer func() {
			<-s.ch
		}()
		return fn()
	})
}

// Wait waits for the group to complete and return an error if any.
func (s *parallelGroup) Wait() error {
	return s.g.Wait()
}