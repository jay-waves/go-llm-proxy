package pipeline

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"go-llm-proxy/internal/config"
)

// InternalKeyStrippedTools is the map key used to pass stripped server tool types
// from the translation layer to the pipeline. Deleted before sending to backend.
const InternalKeyStrippedTools = "_stripped_server_tools"

// Pipeline orchestrates pre-send content processing for translated Chat Completions requests.
// It detects unsupported content (images) and routes them to capable processor models.
type Pipeline struct {
	config *config.ConfigStore
	client *http.Client
}

// NewPipeline creates a pipeline that uses the given config and HTTP client for processor calls.
func NewPipeline(cs *config.ConfigStore, client *http.Client) *Pipeline {
	return &Pipeline{config: cs, client: client}
}

// processingSignatures are byte patterns that indicate a request body may contain
// content that needs pipeline processing. Used for cheap pre-scan before full JSON parse.
var processingSignatures = [][]byte{
	[]byte(`"image_url"`),    // OpenAI image format
	[]byte(`"type":"image"`), // Anthropic image format (after translation)
}

// BodyNeedsProcessing does a fast string scan to detect if the raw request body
// contains content that may need pipeline processing. This avoids full JSON parse
// for the common case of text-only requests.
func (p *Pipeline) BodyNeedsProcessing(body []byte) bool {
	for _, sig := range processingSignatures {
		if bytes.Contains(body, sig) {
			return true
		}
	}
	return false
}

// ShouldProcess returns whether any pipeline stage should run for the given
// model — a fast pre-check so handlers can skip the pipeline entirely when
// no interception stage is enabled.
func (p *Pipeline) ShouldProcess(model *config.ModelConfig) bool {
	return p.shouldRewriteVision(model)
}

// shouldRewriteVision reports whether the proxy should intercept image_url
// content for this model (describe it via the vision cascade) rather than
// forwarding it as-is. Auto follows the "Vision capable" flag — a model that
// can't accept images itself gets the fallback; one that can is left alone.
// Upstream protocol plays no part: an Anthropic-shaped backend is not
// necessarily real Claude, and a non-vision-capable model needs the same
// help regardless of which protocol it speaks.
func (p *Pipeline) shouldRewriteVision(model *config.ModelConfig) bool {
	switch model.RewriteVision {
	case "on":
		return true
	case "off":
		return false
	default:
		return model.ForcePipeline || !model.SupportsVision
	}
}

// resolveVisionProcessors returns the priority-ordered vision cascade for the
// given target model. A per-model override (single model, or "none") beats the
// global cascade. Empty result = vision processing disabled.
func (p *Pipeline) resolveVisionProcessors(targetModel *config.ModelConfig) []string {
	// Per-model override takes precedence.
	if targetModel.Processors != nil {
		if targetModel.Processors.Vision == "none" {
			return nil
		}
		if targetModel.Processors.Vision != "" {
			return []string{targetModel.Processors.Vision}
		}
	}
	// Fall back to the global cascade.
	return p.config.Get().Processors.EffectiveVisionModels()
}

// resolveVisionProcessor returns the head of the vision cascade ("" = disabled).
func (p *Pipeline) resolveVisionProcessor(targetModel *config.ModelConfig) string {
	if l := p.resolveVisionProcessors(targetModel); len(l) > 0 {
		return l[0]
	}
	return ""
}

// ProcessRequest runs pre-send processors on a translated Chat Completions request.
// It modifies the request in place and returns it.
func (p *Pipeline) ProcessRequest(ctx context.Context, chatReq map[string]any,
	targetModel *config.ModelConfig) (map[string]any, error) {

	rewriteVisionEnabled := p.shouldRewriteVision(targetModel)
	if !rewriteVisionEnabled {
		return chatReq, nil
	}

	cfg := p.config.Get()

	// Resolve the vision cascade once for image processing.
	// Entries that are the target itself are dropped (pointless round-trip).
	var visionModels []*config.ModelConfig
	for _, name := range p.resolveVisionProcessors(targetModel) {
		if m := config.FindModel(cfg, name); m != nil && m.Name != targetModel.Name {
			visionModels = append(visionModels, m)
		}
	}
	var visionModel *config.ModelConfig
	if len(visionModels) > 0 {
		visionModel = visionModels[0]
	}

	// Vision: route images to processor if target can't handle them natively,
	// or if rewrite_vision is explicitly "on" (force describing even though
	// the model could accept the image directly). Gated independently of PDF
	// handling — image_url content only.
	if rewriteVisionEnabled && visionModel != nil && (!targetModel.SupportsVision || targetModel.RewriteVision == "on") {
		var err error
		chatReq, err = p.processImages(ctx, chatReq, visionModels)
		if err != nil {
			slog.Warn("vision processing error", "error", err)
		}
	}

	// Clean up internal metadata that shouldn't be sent to the backend.
	delete(chatReq, InternalKeyStrippedTools)

	return chatReq, nil
}

// pipelineError builds a formatted error message for pipeline failures.
func pipelineError(feature, model, docSection, originalErr string) string {
	return fmt.Sprintf(
		"The backend model (%s) does not support %s, and the proxy could not process it.\n\n"+
			"To enable %s support, configure the proxy:\n\n"+
			"    processors:\n"+
			"      %s\n\n"+
			"Original error:\n    %s",
		model, feature, feature, docSection, originalErr,
	)
}

// imageNotSupportedError returns a friendly error when images are sent to a text-only
// model and no vision processor is configured.
func imageNotSupportedError(modelName string, originalErr string) string {
	return pipelineError("image inputs", modelName,
		"vision: your-vision-model    # any vision-capable model", originalErr)
}
