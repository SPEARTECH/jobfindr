package main

import (
	"bufio"
	"context"
	"fmt"
	"html"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	. "github.com/speartech/gophers"
)

var fetchMu sync.Mutex
var fetchDelay = 1500 * time.Millisecond // adjust as needed
var fetchIdx int32

// UDF applies fn to the string value produced by `input` Column for each row.
func UDF(input Column, fn func(string) (interface{}, error)) Column {
	return Column{
		Name: "udf_" + input.Name,
		Fn: func(row map[string]interface{}) interface{} {
			v := input.Fn(row)
			var s string
			switch t := v.(type) {
			case string:
				s = t
			case []byte:
				s = string(t)
			default:
				s = fmt.Sprint(v)
			}
			out, err := fn(s)
			if err != nil {
				fmt.Println("udf error:", err, "input:", s)
				return nil
			}
			return out
		},
	}
}
func main() {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", false), // show browser so you can log in
	)
	allocCtx, _ := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// open LinkedIn login page
	if err := chromedp.Run(ctx, chromedp.Navigate("https://www.linkedin.com/login")); err != nil {
		fmt.Println("navigate error:", err)
		return
	}
    
	fmt.Println("Browser opened. Log in to LinkedIn in the browser window, then press Enter here to continue.")
	bufio.NewReader(os.Stdin).ReadBytes('\n')
    
	// go to saved jobs and grab HTML
	var savedHTML string
	target := "https://www.linkedin.com/my-items/saved-jobs/?cardType=SAVED"
	if err := chromedp.Run(ctx,
		chromedp.Navigate(target),
		chromedp.Sleep(2*time.Second),
		chromedp.WaitVisible("body", chromedp.ByQuery),
		chromedp.OuterHTML("html", &savedHTML, chromedp.ByQuery),
	); err != nil {
		fmt.Println("error:", err)
		return
	}

	// fmt.Println(savedHTML)

	df := ReadHTML(savedHTML)
	df = df.Filter(Col("tag").Eq("ul"))
	df = df.Column("pages_total", Col("inner_html_str").ExtractHTML("span").Index(9))
	var pages_total int
	vals := df.Filter(Col("depth").Eq(10)).Collect("pages_total")
	if len(vals) == 0 {
		fmt.Println("no pages_total rows found; dumping sample df for debugging:")
		df.Vertical(10, 100)
	} else {
		if s, ok := vals[len(vals)-1].(string); ok {
			fmt.Sscanf(s, "%d", &pages_total)
		}
	}
	// test
	// pages_total = 1 // for testing, comment out when done

	df = df.Filter(Col("depth").Eq(11))

	df = ReadHTMLTop(df.Collect("inner_html_str")[0].(string))

	// suppress library prints from df.Count()
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// page_amt := df.Count()

	w.Close()
	os.Stdout = oldStdout
	_, _ = io.ReadAll(r) // discard captured output

	df = df.Column("job_title", Col("inner_html_str").ExtractHTML("a").Index(1))
	df = df.Column("company_name", Col("inner_html_str").ExtractHTML("div").Index(11))
	df = df.Column("location", Col("inner_html_str").ExtractHTML("div").Index(12))
	df = df.Column("post_date", Col("inner_html_str").ExtractHTML("span").Index(5))
	df = df.Column("link", Col("inner_html_str").ExtractHTML("span").Index(1))

	df = df.Column("job_title", Col("job_title").RegexpReplace(`(?s)^.*?>([^<]+)<.*$`, "$1"))
	df = df.Column("company_name", Col("company_name").RegexpReplace(`(?s)^.*?>([^<]+)<.*$`, "$1"))
	df = df.Column("location", Col("location").RegexpReplace(`(?s)^.*?>([^<]+)<.*$`, "$1"))
	df = df.Column("post_date", If(Col("post_date").IsNull(), Col("inner_html_str").ExtractHTML("span").Index(2), Col("post_date")))
	df = df.Column("post_date", Col("post_date").RegexpReplace(`(?s)^.*?>([^<]+)<.*$`, "$1"))
	df = df.Column("link", Col("link").RegexpReplace(`(?s).*?href="([^"]+)".*`, "$1"))

	// iterate through pages if there are more than 1 page of saved jobs
	df = df.Select("job_title", "company_name", "location", "post_date", "link")

	// print page 1 (we already parsed savedHTML into df)
	fmt.Printf("Navigating to page %d/%d...\n", 1, pages_total)

	// determine items per page from the first page
	pageSize := len(df.Collect("link"))

	// fetch remaining pages synchronously, one-by-one, and union them
	for p := 2; p <= pages_total; p++ {
		fmt.Printf("Navigating to page %d/%d...\n", p, pages_total)
		start := (p - 1) * pageSize
		pageURL := fmt.Sprintf("%s&start=%d", target, start)

		var pageHTML string
		if err := chromedp.Run(ctx,
			chromedp.Navigate(pageURL),
			chromedp.Sleep(1*time.Second),
			chromedp.WaitVisible("body", chromedp.ByQuery),
			chromedp.OuterHTML("html", &pageHTML, chromedp.ByQuery),
		); err != nil {
			fmt.Println("navigate error:", err, "url:", pageURL)
			break
		}

		df2 := ReadHTML(pageHTML)
		df2 = df2.Filter(Col("tag").Eq("ul"))
		df2 = df2.Filter(Col("depth").Eq(11))
		df2 = ReadHTMLTop(df2.Collect("inner_html_str")[0].(string))
		df2 = df2.Column("job_title", Col("inner_html_str").ExtractHTML("a").Index(1))
		df2 = df2.Column("company_name", Col("inner_html_str").ExtractHTML("div").Index(11))
		df2 = df2.Column("location", Col("inner_html_str").ExtractHTML("div").Index(12))
		df2 = df2.Column("post_date", Col("inner_html_str").ExtractHTML("span").Index(5))
		df2 = df2.Column("link", Col("inner_html_str").ExtractHTML("span").Index(1))
		df2 = df2.Column("job_title", Col("job_title").RegexpReplace(`(?s)^.*?>([^<]+)<.*$`, "$1"))
		df2 = df2.Column("company_name", Col("company_name").RegexpReplace(`(?s)^.*?>([^<]+)<.*$`, "$1"))
		df2 = df2.Column("location", Col("location").RegexpReplace(`(?s)^.*?>([^<]+)<.*$`, "$1"))
		df2 = df2.Column("post_date", If(Col("post_date").IsNull(), Col("inner_html_str").ExtractHTML("span").Index(2), Col("post_date")))
		df2 = df2.Column("post_date", Col("post_date").RegexpReplace(`(?s)^.*?>([^<]+)<.*$`, "$1"))
		df2 = df2.Column("link", Col("link").RegexpReplace(`(?s).*?href="([^"]+)".*`, "$1"))
		df2 = df2.Select("job_title", "company_name", "location", "post_date", "link")

		df = df.Union(df2)
	}

	// now df contains all pages (no duplicate first-page reads)

	// collect links and fetch each link synchronously in the same ctx (space with fetchDelay)
	links := df.Collect("link")
	pagesByLink := make(map[string]string, len(links))
	totalLinks := len(links)
	for i, v := range links {
		url := fmt.Sprint(v)
		raw := strings.TrimSpace(html.UnescapeString(url))
		if !strings.HasPrefix(raw, "http") {
			raw = "https://www.linkedin.com" + raw
		}
		fmt.Printf("fetching: %d/%d %s\n", i+1, totalLinks, raw)

		var pageHTML string
		if err := chromedp.Run(ctx,
			chromedp.Navigate(raw),
			chromedp.Sleep(1*time.Second),
			chromedp.WaitVisible("body", chromedp.ByQuery),
			chromedp.OuterHTML("html", &pageHTML, chromedp.ByQuery),
		); err != nil {
			fmt.Println("fetch error:", err, "url:", raw)
			pagesByLink[url] = ""
		} else {
			pagesByLink[url] = pageHTML
		}
		time.Sleep(fetchDelay)
	}

	// small UDF that returns the pre-fetched page HTML for a link
	getPrefetched := func(u string) (interface{}, error) {
		raw := strings.TrimSpace(html.UnescapeString(u))
		if !strings.HasPrefix(raw, "http") {
			raw = "https://www.linkedin.com" + raw
		}
		if v, ok := pagesByLink[u]; ok {
			return v, nil
		}
		if v, ok := pagesByLink[raw]; ok {
			return v, nil
		}
		return "", nil
	}

	df = df.Column("link_body", UDF(Col("link"), getPrefetched))
	// df = df.Filter(Col("job_title").Eq("Distinguished Software Engineering Lead")) // temp filter for testing, comment out when done

	df = df.Column("salary", Col("link_body").ExtractHTML("span").Index(38))
	df = df.Column("salary", If(
		Or(
			Col("salary").Contains("<span"),
			Col("salary").Contains("<svg"),
		),
		Col("link_body").ExtractHTML("span").Index(39),
		Col("salary"),
	),
	)
	df = df.Column("salary", Col("salary").RegexpReplace(`(?s).*-\s*\$?([0-9,]+(?:\.[0-9]+)?[KM]?)\/yr.*`, "$1"))
	df = df.Drop("link_body")
	df = df.Column("salary", If(
		Or(
			Col("salary").Eq("Part-time"),
			Col("salary").Eq("Hybrid"),
			Col("salary").Eq("Remote"),
			Col("salary").Eq("On-site"),
			Col("salary").Eq("Full-time"),
			Col("salary").Eq("Easy Apply"),
			Col("salary").Eq("Apply"),
		),
		Lit(""),
		Col("salary")))

	// extract last parenthetical (e.g. "New York (Hybrid)" -> "Hybrid")
	df = df.Column("type", Col("location").RegexpReplace(`(?s).*\(([^)]*)\)[^()]*$`, "$1"))
	df = df.Column("type", If(
		Or(
			Col("type").Eq("Hybrid"),
			Col("type").Eq("Remote"),
			Col("type").Eq("On-site"),
		), Col("type"),
		Lit("")))

	// df.Count()
	df.DisplayBrowser()
	// df.Vertical(100, 100)
}
