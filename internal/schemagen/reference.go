package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/config"
)

// The reference is written in Chinese, like the rest of docs/.

func typeLabel(n node) string {
	if ref, ok := n["$ref"].(string); ok {
		return "`" + strings.TrimPrefix(ref, "#/$defs/") + "`"
	}
	switch n["type"] {
	case "array":
		item := typeLabel(n["items"].(node))
		if strings.HasSuffix(item, "`") {
			return item + " 列表"
		}
		return item + "列表"
	case "object":
		if ap, ok := n["additionalProperties"].(node); ok {
			return "映射（名称 → " + typeLabel(ap) + "）"
		}
		if _, ok := n["properties"]; ok {
			return "对象"
		}
		return "自由对象"
	case "string":
		if n["x-format"] == "duration" {
			return "时长"
		}
		return "字符串"
	case "integer":
		return "整数"
	case "number":
		return "数字"
	case "boolean":
		return "布尔"
	}
	return "任意"
}

func defaultLabel(n node) string {
	v, ok := n["default"]
	if !ok {
		return ""
	}
	b, _ := json.Marshal(v)
	return "`" + string(b) + "`"
}

func constraints(n node, rule config.Rule) string {
	var out []string
	if rule.Required {
		out = append(out, "必填")
	}
	if rule.Enum != nil {
		out = append(out, "可选 "+code(rule.Enum...))
	}
	if rule.Examples != nil {
		out = append(out, "内置 "+code(rule.Examples...))
	}
	if rule.Pattern != "" {
		out = append(out, "格式 "+code(rule.Pattern))
	}
	if rule.Keys != "" {
		out = append(out, "名称格式 "+code(rule.Keys))
	}
	if rule.MinLength > 0 {
		out = append(out, "非空")
	}
	if rule.MinItems > 0 {
		out = append(out, fmt.Sprintf("至少 %d 项", rule.MinItems))
	}
	switch {
	case rule.Min != "" && rule.Max != "":
		out = append(out, rule.Min+"–"+rule.Max)
	case rule.Min != "":
		out = append(out, "≥ "+rule.Min)
	case rule.Max != "":
		out = append(out, "≤ "+rule.Max)
	}
	if rule.Restart {
		out = append(out, "修改需重启")
	}
	return strings.Join(out, "；")
}

func code(values ...string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = "`" + strings.ReplaceAll(v, "|", `\|`) + "`"
	}
	return strings.Join(parts, "、")
}

func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ")
}

// reference renders one table per top-level section and per definition.
func (g *generator) reference() string {
	var b strings.Builder
	b.WriteString("<!-- 本节由 internal/schemagen 根据 core/config 的结构体标签与\n" +
		"core/config/fields.yaml 生成（go generate ./core/config），请勿手改。 -->\n")
	for _, name := range topLevel() {
		g.table(&b, name, "`"+name+"`")
	}
	for _, name := range defOrder {
		g.table(&b, "defs."+name, "定义 `"+name+"`")
	}
	return b.String()
}

func (g *generator) table(b *strings.Builder, section, heading string) {
	d := g.docs[section]
	title := d.Title["zh-CN"]
	if title != "" {
		heading += "：" + title
	}
	fmt.Fprintf(b, "\n### %s\n\n%s\n", heading, d.ZH)
	var rows []field
	for _, r := range g.rows[section] {
		// A section's own row only helps when it is not a plain object.
		if r.Path != section || r.Type != "对象" {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return
	}
	b.WriteString("\n| 字段 | 类型 | 默认值 | 约束 | 说明 |\n| --- | --- | --- | --- | --- |\n")
	for _, r := range rows {
		fmt.Fprintf(b, "| `%s` | %s | %s | %s | %s |\n",
			r.Path, cell(r.Type), cell(r.Default), cell(r.Constraints), cell(r.Doc.ZH))
	}
}

// topLevel lists the configuration sections in declaration order.
func topLevel() []string {
	t := reflect.TypeFor[config.Config]()
	var out []string
	for i := range t.NumField() {
		if name := config.YAMLName(t.Field(i)); name != "" {
			out = append(out, name)
		}
	}
	return out
}
