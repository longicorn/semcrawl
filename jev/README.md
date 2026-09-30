# Jev Go client

This package calls the TypeSafe Jev HTTP API. It does not include browser or
DOM integration.

```go
client := jev.NewClient("") // reads TYPESAFE_API_KEY
result, err := client.Evaluate(ctx, jev.Request{
	State: "The page contains a product titled Example, priced at $12.",
	Questions: map[string]jev.Question{
		"has_product": {
			Type:         jev.QuestionNoul,
			Instructions: "Does the state describe a product?",
		},
	},
})
if err != nil {
	return err
}
fmt.Println(*result.Answers["has_product"].Noul)
```

The model defaults to `jev-latest`. Set `Request.Model` to use a different
model. API failures are returned as `*jev.APIError`, which preserves the HTTP
status code and response body. Use `jev.WithBaseURL` and `jev.WithHTTPClient`
to configure a compatible endpoint or HTTP transport.
