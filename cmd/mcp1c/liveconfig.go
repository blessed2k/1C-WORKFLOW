package main

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// The live variants of the config tools read the connector like every other
// live tool, so they take base too. The offline variants only know the active
// source, and with several bases the server starts with none selected: in the
// journals of 21.09-18.11.2026 they answered "no configuration source" to most
// live calls (get_object_structure 60%, get_metadata_tree 13%) although the
// connector serves all of them.
//
// Outputs carry base with omitempty: these tool names are shared with offline
// mode, where the same fields are pinned as the contract, and a field turning
// required there breaks it.

const liveBaseSentence = " Pass base=<name> to read one base for this call."

type liveConfigurationInfoInput struct {
	Base string `json:"base,omitempty" jsonschema:"run against this base for this call only, without changing the active one (see list_bases)"`
}

type liveConfigurationInfoOutput struct {
	Base string `json:"base,omitempty" jsonschema:"live base this answer came from"`
	source.ConfigurationInfo
}

type liveMetadataTreeInput struct {
	metadataTreeInput
	Base string `json:"base,omitempty" jsonschema:"run against this base for this call only, without changing the active one (see list_bases)"`
}

type liveMetadataTreeOutput struct {
	Base string `json:"base,omitempty" jsonschema:"live base this answer came from"`
	source.MetadataTree
}

type liveObjectExistsInput struct {
	objectExistsInput
	Base string `json:"base,omitempty" jsonschema:"run against this base for this call only, without changing the active one (see list_bases)"`
}

type liveObjectExistsOutput struct {
	Base string `json:"base,omitempty" jsonschema:"live base this answer came from"`
	objectExistsOutput
}

type liveObjectStructureInput struct {
	objectStructureInput
	Base string `json:"base,omitempty" jsonschema:"run against this base for this call only, without changing the active one (see list_bases)"`
}

type liveObjectStructureOutput struct {
	Base string `json:"base,omitempty" jsonschema:"live base this answer came from"`
	objectStructureOutput
}

// registerLiveConfigTools wires get_configuration_info, get_metadata_tree,
// object_exists and get_object_structure over the connector. lp resolves the
// base of a call the way every live tool does.
func registerLiveConfigTools(server *mcp.Server, lp liveProvider) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_configuration_info",
		Description: configurationInfoDescription + " Live, it also reports the connector version and features." + liveBaseSentence,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in liveConfigurationInfoInput) (*mcp.CallToolResult, liveConfigurationInfoOutput, error) {
		live, name, err := resolveLive(lp, in.Base)
		if err != nil {
			return nil, liveConfigurationInfoOutput{}, err
		}
		info, err := live.ConfigurationInfo(ctx)
		if err != nil {
			return nil, liveConfigurationInfoOutput{}, fmt.Errorf("base %s: %w", name, err)
		}
		return nil, liveConfigurationInfoOutput{Base: name, ConfigurationInfo: *info}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_metadata_tree",
		Description: metadataTreeDescription + liveBaseSentence,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in liveMetadataTreeInput) (*mcp.CallToolResult, liveMetadataTreeOutput, error) {
		live, name, err := resolveLive(lp, in.Base)
		if err != nil {
			return nil, liveMetadataTreeOutput{}, err
		}
		tree, err := metadataTreeAnswer(ctx, live, in.metadataTreeInput)
		if err != nil {
			return nil, liveMetadataTreeOutput{}, fmt.Errorf("base %s: %w", name, err)
		}
		return nil, liveMetadataTreeOutput{Base: name, MetadataTree: tree}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "object_exists",
		Description: objectExistsDescription + liveBaseSentence,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in liveObjectExistsInput) (*mcp.CallToolResult, liveObjectExistsOutput, error) {
		live, name, err := resolveLive(lp, in.Base)
		if err != nil {
			return nil, liveObjectExistsOutput{}, err
		}
		out, err := objectExistsAnswer(ctx, live, in.objectExistsInput)
		if err != nil {
			return nil, liveObjectExistsOutput{}, fmt.Errorf("base %s: %w", name, err)
		}
		return nil, liveObjectExistsOutput{Base: name, objectExistsOutput: out}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_object_structure",
		Description: objectStructureDescription + liveBaseSentence,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in liveObjectStructureInput) (*mcp.CallToolResult, liveObjectStructureOutput, error) {
		live, name, err := resolveLive(lp, in.Base)
		if err != nil {
			return nil, liveObjectStructureOutput{}, err
		}
		out, err := objectStructureAnswer(ctx, server, live, in.objectStructureInput)
		if err != nil {
			return nil, liveObjectStructureOutput{}, fmt.Errorf("base %s: %w", name, err)
		}
		return nil, liveObjectStructureOutput{Base: name, objectStructureOutput: out}, nil
	})
}
