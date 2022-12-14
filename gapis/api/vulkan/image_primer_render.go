// Copyright (C) 2019 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vulkan

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/google/gapid/core/log"
	"github.com/google/gapid/gapis/memory"
)

const (
	ipRenderInputAttachmentBinding       = 0
	ipRenderInitialDescriptorSetPoolSize = 16
	ipRenderInputAttachmentIndex         = 0
	ipRenderOutputAttachmentIndex        = 1
	ipRenderInputAttachmentLayout        = VkImageLayout_VK_IMAGE_LAYOUT_SHADER_READ_ONLY_OPTIMAL
	ipRenderColorOutputLayout            = VkImageLayout_VK_IMAGE_LAYOUT_COLOR_ATTACHMENT_OPTIMAL
	ipRenderDepthStencilOutputLayout     = VkImageLayout_VK_IMAGE_LAYOUT_DEPTH_STENCIL_ATTACHMENT_OPTIMAL
)

var (
	// initialized in init()
	descriptorSetLayoutInfoForRender ipDescriptorSetLayoutInfo
)

func init() {
	descriptorSetLayoutInfoForRender.bindings = map[uint32]ipDescriptorSetLayoutBindingInfo{}
	descriptorSetLayoutInfoForRender.bindings[ipRenderInputAttachmentBinding] = ipDescriptorSetLayoutBindingInfo{
		VkDescriptorType_VK_DESCRIPTOR_TYPE_INPUT_ATTACHMENT,
		1, VkShaderStageFlags(VkShaderStageFlagBits_VK_SHADER_STAGE_FRAGMENT_BIT),
	}
}

// ipRenderRecipe describes how a subresource region of an input attachment
// image should be rendered to the corresponding region of the render target
// image.
type ipRenderRecipe struct {
	inputAttachmentImage  VkImage
	inputAttachmentAspect VkImageAspectFlagBits
	renderImage           VkImage
	renderAspect          VkImageAspectFlagBits
	layer                 uint32
	level                 uint32
	renderRectX           int32
	renderRectY           int32
	renderRectWidth       uint32
	renderRectHeight      uint32
	wordIndex             uint32
	framebufferWidth      uint32
	framebufferHeight     uint32
}

// ipRenderKitBuilder builds the kits used to generate commands to prime image
// data by rendering from input attachment image.
type ipRenderKitBuilder struct {
	nm                  debugMarkerName
	dev                 VkDevice
	descriptorSetLayout VkDescriptorSetLayout
	pipelineLayout      VkPipelineLayout
	descSetPool         *homoDescriptorSetPool
	shaderModulePool    *naiveShaderModulePool
	imageViewPool       *naiveImageViewPool
	// Use a pair of map + slice to be able to easily iterate over
	// the resources in order when we free them, such that trace
	// linearization is deterministic.
	renderPassPoolIndex  map[ipRenderRenderPassInfo]int
	renderPassPool       []VkRenderPass
	pipelinePoolIndex    map[ipRenderPipelineInfo]int
	pipelinePool         []VkPipeline
	framebufferPoolIndex map[ipRenderFramebufferInfo]int
	framebufferPool      []VkFramebuffer
}

func newImagePrimerRenderKitBuilder(sb *stateBuilder, dev VkDevice) *ipRenderKitBuilder {
	builder := &ipRenderKitBuilder{
		nm:                   debugMarkerName(fmt.Sprintf("render kit builder dev: %v", dev)),
		dev:                  dev,
		shaderModulePool:     newNaiveShaderModulePool(dev),
		imageViewPool:        newNaiveImageViewPool(dev),
		renderPassPoolIndex:  map[ipRenderRenderPassInfo]int{},
		renderPassPool:       []VkRenderPass{},
		pipelinePoolIndex:    map[ipRenderPipelineInfo]int{},
		pipelinePool:         []VkPipeline{},
		framebufferPoolIndex: map[ipRenderFramebufferInfo]int{},
		framebufferPool:      []VkFramebuffer{},
	}
	builder.descriptorSetLayout = ipCreateDescriptorSetLayout(sb, builder.nm, dev, descriptorSetLayoutInfoForRender)
	builder.descSetPool = newHomoDescriptorSetPool(sb, builder.nm, dev, builder.descriptorSetLayout, ipRenderInitialDescriptorSetPoolSize, false)
	builder.pipelineLayout = ipCreatePipelineLayout(sb, builder.nm, dev,
		[]VkDescriptorSetLayout{builder.descriptorSetLayout},
		VkShaderStageFlags(VkShaderStageFlagBits_VK_SHADER_STAGE_FRAGMENT_BIT), 4)
	return builder
}

// Free frees all the resources used by all the kits generated from this builder.
func (kb *ipRenderKitBuilder) Free(sb *stateBuilder) {
	if kb.descSetPool != nil {
		kb.descSetPool.Free(sb)
		kb.descSetPool = nil
	}
	for _, f := range kb.framebufferPool {
		sb.write(sb.cb.VkDestroyFramebuffer(kb.dev, f, memory.Nullptr))
	}
	kb.framebufferPoolIndex = map[ipRenderFramebufferInfo]int{}
	kb.framebufferPool = []VkFramebuffer{}
	if kb.imageViewPool != nil {
		kb.imageViewPool.Free(sb)
		kb.imageViewPool = nil
	}
	for _, p := range kb.pipelinePool {
		sb.write(sb.cb.VkDestroyPipeline(kb.dev, p, memory.Nullptr))
	}
	kb.pipelinePoolIndex = map[ipRenderPipelineInfo]int{}
	kb.pipelinePool = []VkPipeline{}
	if kb.shaderModulePool != nil {
		kb.shaderModulePool.Free(sb)
		kb.shaderModulePool = nil
	}
	for _, r := range kb.renderPassPool {
		sb.write(sb.cb.VkDestroyRenderPass(kb.dev, r, memory.Nullptr))
	}
	kb.renderPassPoolIndex = map[ipRenderRenderPassInfo]int{}
	kb.renderPassPool = []VkRenderPass{}
	if kb.pipelineLayout != VkPipelineLayout(0) {
		sb.write(sb.cb.VkDestroyPipelineLayout(kb.dev, kb.pipelineLayout, memory.Nullptr))
		kb.pipelineLayout = VkPipelineLayout(0)
	}
	if kb.descriptorSetLayout != VkDescriptorSetLayout(0) {
		sb.write(sb.cb.VkDestroyDescriptorSetLayout(
			kb.dev, kb.descriptorSetLayout, memory.Nullptr))
		kb.descriptorSetLayout = VkDescriptorSetLayout(0)
	}
}

// BuildRenderKits takes a list of render recipes and returns a list of render
// kits that contains the commands to render the input attachments specified in
// the recipes to the render target images.
func (kb *ipRenderKitBuilder) BuildRenderKits(sb *stateBuilder, recipes ...ipRenderRecipe) ([]ipRenderKit, error) {
	var err error
	renderCount := uint32(len(recipes))
	kits := make([]ipRenderKit, renderCount)
	// reserve and update descriptor sets
	descSetReservation, err := kb.descSetPool.ReserveDescriptorSets(sb, renderCount)
	if err != nil {
		return []ipRenderKit{}, log.Errf(sb.ctx, err, "failed at reserving %v descriptor sets", renderCount)
	}
	descSets := descSetReservation.DescriptorSets()
	if len(descSets) != len(recipes) {
		return []ipRenderKit{}, fmt.Errorf("not enough reserved descriptor sets")
	}
	for i := range kits {
		kits[i].dependentPieces = append(kits[i].dependentPieces, descSetReservation)
		des := descSets[i]
		inputView := kb.imageViewPool.getOrCreateImageView(sb, kb.nm, ipImageViewInfo{
			image:  recipes[i].inputAttachmentImage,
			aspect: recipes[i].inputAttachmentAspect,
			layer:  recipes[i].layer,
			level:  recipes[i].level,
		})
		writeDescriptorSet(sb, kb.dev, des, ipRenderInputAttachmentBinding, 0,
			VkDescriptorType_VK_DESCRIPTOR_TYPE_INPUT_ATTACHMENT,
			[]VkDescriptorImageInfo{
				NewVkDescriptorImageInfo(
					0,                             // sampler
					inputView,                     // image view
					ipRenderInputAttachmentLayout, // layout
				)}, []VkDescriptorBufferInfo{}, []VkBufferView{})
		kits[i].descriptorSet = des
	}
	for i := range kits {
		inputImgObj := GetState(sb.newState).Images().Get(recipes[i].inputAttachmentImage)
		outputImgObj := GetState(sb.newState).Images().Get(recipes[i].renderImage)
		// set pipeline
		kits[i].pipelineLayout = kb.pipelineLayout
		kits[i].pipeline = kb.getOrCreatePipeline(sb, ipRenderPipelineInfo{
			inputAttachmentFormat: inputImgObj.Info().Fmt(),
			outputAspect:          recipes[i].renderAspect,
			outputFormat:          outputImgObj.Info().Fmt(),
			pipelineLayout:        kb.pipelineLayout,
		})
		// set renderpass
		kits[i].renderPass = kb.getOrCreateRenderPass(sb, ipRenderRenderPassInfo{
			inputAttachmentFormat: inputImgObj.Info().Fmt(),
			outputFormat:          outputImgObj.Info().Fmt(),
			outputAspect:          recipes[i].renderAspect,
		})
		kits[i].renderRectX = recipes[i].renderRectX
		kits[i].renderRectY = recipes[i].renderRectY
		kits[i].renderRectWidth = recipes[i].renderRectWidth
		kits[i].renderRectHeight = recipes[i].renderRectHeight
		// set framebuffer
		kits[i].framebuffer = kb.getOrCreateFramebuffer(sb, ipRenderFramebufferInfo{
			inputAttachmentImage:  recipes[i].inputAttachmentImage,
			inputAttachmentAspect: VkImageAspectFlagBits_VK_IMAGE_ASPECT_COLOR_BIT,
			outputImage:           recipes[i].renderImage,
			outputAspect:          recipes[i].renderAspect,
			layer:                 recipes[i].layer,
			level:                 recipes[i].level,
			width:                 recipes[i].framebufferWidth,
			height:                recipes[i].framebufferHeight,
		})
		kits[i].framebufferWidth = recipes[i].framebufferWidth
		kits[i].framebufferHeight = recipes[i].framebufferHeight
		// set stencil
		kits[i].stencil = recipes[i].renderAspect == VkImageAspectFlagBits_VK_IMAGE_ASPECT_STENCIL_BIT
		// set name
		kits[i].name = debugMarkerName(
			fmt.Sprintf("Render target img: %v, aspect: %v, layer: %v, level: %v",
				recipes[i].renderImage, recipes[i].renderAspect, recipes[i].layer, recipes[i].level))
	}

	return kits, nil
}

// ipRenderKit contains all the necessary resources to begin a render pass to
// prime image data by rendering.
type ipRenderKit struct {
	name              debugMarkerName
	stencil           bool
	renderRectX       int32
	renderRectY       int32
	renderRectWidth   uint32
	renderRectHeight  uint32
	framebufferWidth  uint32
	framebufferHeight uint32
	renderPass        VkRenderPass
	framebuffer       VkFramebuffer
	pipeline          VkPipeline
	pipelineLayout    VkPipelineLayout
	descriptorSet     VkDescriptorSet
	dependentPieces   []flushablePiece
}

// BuildRenderCommands generates a queue command batch which when being
// committed to a queue command handler, will begin a renderpass and draw, to
// prime the data by rendering.
func (kit ipRenderKit) BuildRenderCommands(sb *stateBuilder) *queueCommandBatch {
	cmdBatch := newQueueCommandBatch(kit.name.String())
	cmdBatch.RecordCommandsOnCommit(func(commandBuffer VkCommandBuffer) {
		sb.write(sb.cb.VkCmdBeginRenderPass(
			commandBuffer,
			sb.MustAllocReadData(
				NewVkRenderPassBeginInfo(
					VkStructureType_VK_STRUCTURE_TYPE_RENDER_PASS_BEGIN_INFO, // sType
					NewVoid??????(memory.Nullptr),                                // pNext
					kit.renderPass,                                           // renderPass
					kit.framebuffer,                                          // framebuffer
					NewVkRect2D( // renderArea
						NewVkOffset2D(kit.renderRectX, kit.renderRectY),
						NewVkExtent2D(kit.renderRectWidth, kit.renderRectHeight),
					),
					0, // clearValueCount
					0, // pClearValues
				)).Ptr(),
			VkSubpassContents(0),
		))
	})
	if kit.stencil {
		cmdBatch.RecordCommandsOnCommit(func(commandBuffer VkCommandBuffer) {
			sb.write(sb.cb.VkCmdClearAttachments(
				commandBuffer,
				uint32(1),
				sb.MustAllocReadData([]VkClearAttachment{
					NewVkClearAttachment(
						VkImageAspectFlags(VkImageAspectFlagBits_VK_IMAGE_ASPECT_STENCIL_BIT), // aspectMask
						0,                  // colorAttachment
						MakeVkClearValue(), // clearValue
					),
				}).Ptr(),
				uint32(1),
				sb.MustAllocReadData([]VkClearRect{
					NewVkClearRect(
						NewVkRect2D(
							NewVkOffset2D(kit.renderRectX, kit.renderRectY),
							NewVkExtent2D(kit.renderRectWidth, kit.renderRectHeight),
						), // rect
						// the baseArrayLayer counts from the base layer of the
						// attachment image view.
						0, // baseArrayLayer
						1, // layerCount
					),
				}).Ptr(),
			))
		})
	}
	cmdBatch.RecordCommandsOnCommit(func(commandBuffer VkCommandBuffer) {
		sb.write(sb.cb.VkCmdBindPipeline(
			commandBuffer,
			VkPipelineBindPoint_VK_PIPELINE_BIND_POINT_GRAPHICS,
			kit.pipeline,
		))
		sb.write(sb.cb.VkCmdSetViewport(
			commandBuffer,
			uint32(0),
			uint32(1),
			NewVkViewport??????(sb.MustAllocReadData(NewVkViewport(
				0, 0, // x, y
				float32(kit.framebufferWidth), float32(kit.framebufferHeight), // width, height
				0, 1, // minDepth, maxDepth
			)).Ptr()),
		))
		sb.write(sb.cb.VkCmdSetScissor(
			commandBuffer,
			0, 1, NewVkRect2D??????(sb.MustAllocReadData(NewVkRect2D(
				NewVkOffset2D(kit.renderRectX, kit.renderRectY),
				NewVkExtent2D(kit.renderRectWidth, kit.renderRectHeight),
			)).Ptr()),
		))
		sb.write(sb.cb.VkCmdBindDescriptorSets(
			commandBuffer,
			VkPipelineBindPoint_VK_PIPELINE_BIND_POINT_GRAPHICS,
			kit.pipelineLayout,
			0,
			1,
			sb.MustAllocReadData(kit.descriptorSet).Ptr(),
			0,
			NewU32??????(memory.Nullptr),
		))
	})
	if kit.stencil {
		cmdBatch.RecordCommandsOnCommit(func(commandBuffer VkCommandBuffer) {
			for i := uint32(0); i < uint32(8); i++ {
				var stencilIndexData bytes.Buffer
				binary.Write(&stencilIndexData, binary.LittleEndian, []uint32{i})
				sb.write(sb.cb.VkCmdSetStencilWriteMask(
					commandBuffer,
					VkStencilFaceFlags(VkStencilFaceFlagBits_VK_STENCIL_FRONT_AND_BACK),
					0x1<<i, // stencil write mask
				))
				sb.write(sb.cb.VkCmdSetStencilReference(
					commandBuffer,
					VkStencilFaceFlags(VkStencilFaceFlagBits_VK_STENCIL_FRONT_AND_BACK),
					0x1<<i, // stencil reference
				))
				sb.write(sb.cb.VkCmdPushConstants(
					commandBuffer,
					kit.pipelineLayout,
					VkShaderStageFlags(VkShaderStageFlagBits_VK_SHADER_STAGE_FRAGMENT_BIT),
					0,
					4,
					NewChar??????(sb.MustAllocReadData(stencilIndexData.Bytes()).Ptr()),
				))
				sb.write(sb.cb.VkCmdDraw(
					commandBuffer,
					6, 1, 0, 0,
				))
			}
		})

	} else {
		cmdBatch.RecordCommandsOnCommit(func(commandBuffer VkCommandBuffer) {
			sb.write(sb.cb.VkCmdDraw(
				commandBuffer,
				6, 1, 0, 0,
			))
		})
	}
	cmdBatch.RecordCommandsOnCommit(func(commandBuffer VkCommandBuffer) {
		sb.write(sb.cb.VkCmdEndRenderPass(commandBuffer))
	})
	cmdBatch.DoOnCommit(func(qch *queueCommandHandler) {
		qch.AddDependentFlushablePieces(kit.dependentPieces...)
	})
	return cmdBatch
}

type ipRenderRenderPassInfo struct {
	inputAttachmentFormat VkFormat
	outputAspect          VkImageAspectFlagBits
	outputFormat          VkFormat
}

func (kb *ipRenderKitBuilder) getOrCreateRenderPass(sb *stateBuilder, info ipRenderRenderPassInfo) VkRenderPass {
	if i, ok := kb.renderPassPoolIndex[info]; ok {
		return kb.renderPassPool[i]
	}

	inputRef := NewVkAttachmentReference(ipRenderInputAttachmentIndex,
		ipRenderInputAttachmentLayout)
	outputRef := NewVkAttachmentReference(ipRenderOutputAttachmentIndex,
		ipRenderColorOutputLayout)

	inputDesc := NewVkAttachmentDescription(
		0,                          // flags
		info.inputAttachmentFormat, // format
		VkSampleCountFlagBits_VK_SAMPLE_COUNT_1_BIT,          // samples
		VkAttachmentLoadOp_VK_ATTACHMENT_LOAD_OP_LOAD,        // loadOp
		VkAttachmentStoreOp_VK_ATTACHMENT_STORE_OP_DONT_CARE, // storeOp
		VkAttachmentLoadOp_VK_ATTACHMENT_LOAD_OP_DONT_CARE,   // stencilLoadOp
		VkAttachmentStoreOp_VK_ATTACHMENT_STORE_OP_DONT_CARE, // stencilStoreOp
		ipRenderInputAttachmentLayout,                        // initialLayout
		ipRenderInputAttachmentLayout,                        // finalLayout
	)
	outputDesc := NewVkAttachmentDescription(
		0,                 // flags
		info.outputFormat, // format
		VkSampleCountFlagBits_VK_SAMPLE_COUNT_1_BIT,        // samples
		VkAttachmentLoadOp_VK_ATTACHMENT_LOAD_OP_DONT_CARE, // loadOp
		VkAttachmentStoreOp_VK_ATTACHMENT_STORE_OP_STORE,   // storeOp
		// Keep the stencil aspect data. When rendering color or depth aspect,
		// stencil test will be disabled so stencil data won't be modified.
		VkAttachmentLoadOp_VK_ATTACHMENT_LOAD_OP_LOAD,    // stencilLoadOp
		VkAttachmentStoreOp_VK_ATTACHMENT_STORE_OP_STORE, // stencilStoreOp
		ipRenderColorOutputLayout,                        // initialLayout
		ipRenderColorOutputLayout,                        // finalLayout
	)
	subpassDesc := NewVkSubpassDescription(
		0, // flags
		VkPipelineBindPoint_VK_PIPELINE_BIND_POINT_GRAPHICS, // pipelineBindPoint
		uint32(1), // inputAttachmentCount
		NewVkAttachmentReference??????(sb.MustAllocReadData(inputRef).Ptr()), // pInputAttachments
		0, // colorAttachmentCount
		// color/depthstencil attachments will be set later according to the
		// aspect bit.
		0, // pColorAttachments
		0, // pResolveAttachments
		0, // pDepthStencilAttachment
		0, // preserveAttachmentCount
		0, // pPreserveAttachments
	)

	switch info.outputAspect {
	case VkImageAspectFlagBits_VK_IMAGE_ASPECT_DEPTH_BIT,
		VkImageAspectFlagBits_VK_IMAGE_ASPECT_STENCIL_BIT:
		{
			outputRef.SetLayout(ipRenderDepthStencilOutputLayout)
			outputDesc.SetInitialLayout(ipRenderDepthStencilOutputLayout)
			outputDesc.SetFinalLayout(ipRenderDepthStencilOutputLayout)
			subpassDesc.SetPDepthStencilAttachment(NewVkAttachmentReference??????(sb.MustAllocReadData(outputRef).Ptr()))
		}
	default:
		{
			subpassDesc.SetColorAttachmentCount(1)
			subpassDesc.SetPColorAttachments(NewVkAttachmentReference??????(sb.MustAllocReadData(outputRef).Ptr()))
		}
	}

	createInfo := NewVkRenderPassCreateInfo(
		VkStructureType_VK_STRUCTURE_TYPE_RENDER_PASS_CREATE_INFO, // sType
		0,         // pNext
		0,         // flags
		uint32(2), // attachmentCount
		NewVkAttachmentDescription??????(sb.MustAllocReadData( // pAttachments
			[]VkAttachmentDescription{inputDesc, outputDesc},
		).Ptr()),
		1, // subpassCount
		NewVkSubpassDescription??????(sb.MustAllocReadData(subpassDesc).Ptr()), // pSubpasses
		0, // dependencyCount
		0, // pDependencies
	)

	handle := VkRenderPass(newUnusedID(true, func(x uint64) bool {
		return GetState(sb.newState).RenderPasses().Contains(VkRenderPass(x)) ||
			GetState(sb.oldState).RenderPasses().Contains(VkRenderPass(x))
	}))

	sb.write(sb.cb.VkCreateRenderPass(
		kb.dev,
		NewVkRenderPassCreateInfo??????(sb.MustAllocReadData(createInfo).Ptr()),
		memory.Nullptr,
		sb.MustAllocWriteData(handle).Ptr(),
		VkResult_VK_SUCCESS,
	))
	if len(kb.nm) > 0 {
		attachDebugMarkerName(sb, kb.nm, kb.dev, handle)
	}
	kb.renderPassPoolIndex[info] = len(kb.renderPassPool)
	kb.renderPassPool = append(kb.renderPassPool, handle)
	return handle
}

type ipRenderPipelineInfo struct {
	inputAttachmentFormat VkFormat
	outputFormat          VkFormat
	outputAspect          VkImageAspectFlagBits
	pipelineLayout        VkPipelineLayout
}

func (kb *ipRenderKitBuilder) getOrCreatePipeline(sb *stateBuilder, info ipRenderPipelineInfo) VkPipeline {
	if i, ok := kb.pipelinePoolIndex[info]; ok {
		return kb.pipelinePool[i]
	}
	rpInfo := ipRenderRenderPassInfo{
		inputAttachmentFormat: info.inputAttachmentFormat,
		outputAspect:          info.outputAspect,
		outputFormat:          info.outputFormat,
	}
	rp := kb.getOrCreateRenderPass(sb, rpInfo)
	vsInfo := ipShaderModuleInfo{
		stage:        VkShaderStageFlagBits_VK_SHADER_STAGE_VERTEX_BIT,
		inputFormat:  info.inputAttachmentFormat,
		outputFormat: info.outputFormat,
		outputAspect: info.outputAspect,
	}
	fsInfo := ipShaderModuleInfo{
		stage:        VkShaderStageFlagBits_VK_SHADER_STAGE_FRAGMENT_BIT,
		inputFormat:  info.inputAttachmentFormat,
		outputFormat: info.outputFormat,
		outputAspect: info.outputAspect,
	}
	vs := kb.shaderModulePool.getOrCreateShaderModule(sb, kb.nm, vsInfo)
	fs := kb.shaderModulePool.getOrCreateShaderModule(sb, kb.nm, fsInfo)

	numColorAttachments := uint32(1)
	depthTestEnable := VkBool32(0)
	depthWriteEnable := VkBool32(0)
	stencilTestEnable := VkBool32(0)
	dynamicStates := []VkDynamicState{
		VkDynamicState_VK_DYNAMIC_STATE_VIEWPORT,
		VkDynamicState_VK_DYNAMIC_STATE_SCISSOR,
	}

	switch info.outputAspect {
	case VkImageAspectFlagBits_VK_IMAGE_ASPECT_DEPTH_BIT:
		depthTestEnable = VkBool32(1)
		depthWriteEnable = VkBool32(1)
		numColorAttachments = uint32(0)
	case VkImageAspectFlagBits_VK_IMAGE_ASPECT_STENCIL_BIT:
		stencilTestEnable = VkBool32(1)
		numColorAttachments = uint32(0)
		dynamicStates = append(dynamicStates,
			VkDynamicState_VK_DYNAMIC_STATE_STENCIL_WRITE_MASK,
			VkDynamicState_VK_DYNAMIC_STATE_STENCIL_REFERENCE,
		)
	default:
	}

	depethStencilState := NewVkPipelineDepthStencilStateCreateInfo(
		VkStructureType_VK_STRUCTURE_TYPE_PIPELINE_DEPTH_STENCIL_STATE_CREATE_INFO, // sType
		0,                                // pNext
		0,                                // flags
		depthTestEnable,                  // depthTestEnable
		depthWriteEnable,                 // depthWriteEnable
		VkCompareOp_VK_COMPARE_OP_ALWAYS, // depthCompareOp
		0,                                // depthBoundsTestEnable
		stencilTestEnable,
		NewVkStencilOpState( // front
			VkStencilOp_VK_STENCIL_OP_KEEP,    // failOp
			VkStencilOp_VK_STENCIL_OP_REPLACE, // passOp
			VkStencilOp_VK_STENCIL_OP_REPLACE, // depthFailOp
			VkCompareOp_VK_COMPARE_OP_ALWAYS,  // compareOp
			0,                                 // compareMask
			// write mask and reference must be set dynamically
			0, // writeMask
			0, // reference
		),
		NewVkStencilOpState(
			0, // failOp
			0, // passOp
			0, // depthFailOp
			0, // compareOp
			0, // compareMask
			0, // writeMask
			0, // reference
		), // back
		0.0, // minDepthBounds
		0.0, // maxDepthBounds
	)
	createInfo := NewVkGraphicsPipelineCreateInfo(
		VkStructureType_VK_STRUCTURE_TYPE_GRAPHICS_PIPELINE_CREATE_INFO, // sType
		0, // pNext
		0, // flags
		2, // stageCount
		NewVkPipelineShaderStageCreateInfo??????(sb.MustAllocReadData( // pStages
			[]VkPipelineShaderStageCreateInfo{
				NewVkPipelineShaderStageCreateInfo(
					VkStructureType_VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO, // sType
					0, // pNext
					0, // flags
					VkShaderStageFlagBits_VK_SHADER_STAGE_VERTEX_BIT, // stage
					vs, // module
					NewChar??????(sb.MustAllocReadData("main").Ptr()), // pName
					NewVkSpecializationInfo??????(memory.Nullptr),     // pSpecializationInfo
				),
				NewVkPipelineShaderStageCreateInfo(
					VkStructureType_VK_STRUCTURE_TYPE_PIPELINE_SHADER_STAGE_CREATE_INFO, // sType
					0, // pNext
					0, // flags
					VkShaderStageFlagBits_VK_SHADER_STAGE_FRAGMENT_BIT, // stage
					fs, // module
					NewChar??????(sb.MustAllocReadData("main").Ptr()), // pName
					NewVkSpecializationInfo??????(memory.Nullptr),     // pSpecializationInfo
				),
			}).Ptr()),
		NewVkPipelineVertexInputStateCreateInfo??????(sb.MustAllocReadData( // pVertexInputState
			NewVkPipelineVertexInputStateCreateInfo(
				VkStructureType_VK_STRUCTURE_TYPE_PIPELINE_VERTEX_INPUT_STATE_CREATE_INFO, // sType
				0, // pNext
				0, // flags
				0, // vertexBindingDescriptionCount
				NewVkVertexInputBindingDescription??????(memory.Nullptr), // pVertexBindingDescriptions
				0, // vertexAttributeDescriptionCouny
				NewVkVertexInputAttributeDescription??????(memory.Nullptr),
			)).Ptr()),
		NewVkPipelineInputAssemblyStateCreateInfo??????(sb.MustAllocReadData( // pInputAssemblyState
			NewVkPipelineInputAssemblyStateCreateInfo(
				VkStructureType_VK_STRUCTURE_TYPE_PIPELINE_INPUT_ASSEMBLY_STATE_CREATE_INFO, // sType
				0, // pNext
				0, // flags
				VkPrimitiveTopology_VK_PRIMITIVE_TOPOLOGY_TRIANGLE_LIST, // topology
				0, // primitiveRestartEnable
			)).Ptr()),
		0, // pTessellationState
		NewVkPipelineViewportStateCreateInfo??????(sb.MustAllocReadData( // pViewportState
			NewVkPipelineViewportStateCreateInfo(
				VkStructureType_VK_STRUCTURE_TYPE_PIPELINE_VIEWPORT_STATE_CREATE_INFO, // sType
				0, // pNext
				0, // flags
				1, // viewportCount
				// set viewport dynamically
				0, // pViewports
				1, // scissorCount
				// set scissor dynamically
				0, // pScissors
			)).Ptr()),
		NewVkPipelineRasterizationStateCreateInfo??????(sb.MustAllocReadData( // pRasterizationState
			NewVkPipelineRasterizationStateCreateInfo(
				VkStructureType_VK_STRUCTURE_TYPE_PIPELINE_RASTERIZATION_STATE_CREATE_INFO, // sType
				0,                                  // pNext
				0,                                  // flags
				0,                                  // depthClampEnable
				0,                                  // rasterizerDiscardEnable
				VkPolygonMode_VK_POLYGON_MODE_FILL, // polygonMode
				VkCullModeFlags(VkCullModeFlagBits_VK_CULL_MODE_BACK_BIT), // cullMode
				VkFrontFace_VK_FRONT_FACE_COUNTER_CLOCKWISE,               // frontFace
				0, // depthBiasEnable
				0, // depthBiasConstantFactor
				0, // depthBiasClamp
				0, // depthBiasSlopeFactor
				1, // lineWidth
			)).Ptr()),
		NewVkPipelineMultisampleStateCreateInfo??????(sb.MustAllocReadData( // pMultisampleState
			NewVkPipelineMultisampleStateCreateInfo(
				VkStructureType_VK_STRUCTURE_TYPE_PIPELINE_MULTISAMPLE_STATE_CREATE_INFO, // sType
				0, // pNext
				0, // flags
				VkSampleCountFlagBits_VK_SAMPLE_COUNT_1_BIT, // rasterizationSamples
				0, // sampleShadingEnable
				0, // minSampleShading
				0, // pSampleMask
				0, // alphaToCoverageEnable
				0, // alphaToOneEnable
			)).Ptr()),
		NewVkPipelineDepthStencilStateCreateInfo??????(sb.MustAllocReadData(depethStencilState).Ptr()), // pDepthStencilState
		NewVkPipelineColorBlendStateCreateInfo??????(sb.MustAllocReadData( // pColorBlendState
			NewVkPipelineColorBlendStateCreateInfo(
				VkStructureType_VK_STRUCTURE_TYPE_PIPELINE_COLOR_BLEND_STATE_CREATE_INFO, // sType
				0,                           // pNext
				0,                           // flags
				0,                           // logicOpEnable
				VkLogicOp_VK_LOGIC_OP_CLEAR, // logicOp
				numColorAttachments,         // attachmentCount
				// there is at most one color attachment
				NewVkPipelineColorBlendAttachmentState??????(sb.MustAllocReadData( // pAttachments
					NewVkPipelineColorBlendAttachmentState(
						0,                                  // blendEnable
						VkBlendFactor_VK_BLEND_FACTOR_ZERO, // srcColorBlendFactor
						VkBlendFactor_VK_BLEND_FACTOR_ONE,  // dstColorBlendFactor
						VkBlendOp_VK_BLEND_OP_ADD,          // colorBlendOp
						VkBlendFactor_VK_BLEND_FACTOR_ZERO, // srcAlphaBlendFactor
						VkBlendFactor_VK_BLEND_FACTOR_ONE,  // dstAlphaBlendFactor
						VkBlendOp_VK_BLEND_OP_ADD,          // alphaBlendOp
						0xf,                                // colorWriteMask
					)).Ptr()),
				NilF32??4???, // blendConstants
			)).Ptr()),
		NewVkPipelineDynamicStateCreateInfo??????(sb.MustAllocReadData( // pDynamicState
			NewVkPipelineDynamicStateCreateInfo(
				VkStructureType_VK_STRUCTURE_TYPE_PIPELINE_DYNAMIC_STATE_CREATE_INFO, // sType
				0,                          // pNext
				0,                          // flags
				uint32(len(dynamicStates)), // dynamicStateCount
				NewVkDynamicState??????(sb.MustAllocReadData(dynamicStates).Ptr()), // pDynamicStates
			)).Ptr()),
		info.pipelineLayout, // layout
		rp,                  // renderPass
		0,                   // subpass
		0,                   // basePipelineHandle
		0,                   // basePipelineIndex
	)

	handle := VkPipeline(newUnusedID(true, func(x uint64) bool {
		return GetState(sb.newState).GraphicsPipelines().Contains(VkPipeline(x)) ||
			GetState(sb.oldState).GraphicsPipelines().Contains(VkPipeline(x))
	}))
	sb.write(sb.cb.VkCreateGraphicsPipelines(
		kb.dev, VkPipelineCache(0), uint32(1),
		sb.MustAllocReadData(createInfo).Ptr(),
		memory.Nullptr, sb.MustAllocWriteData(handle).Ptr(),
		VkResult_VK_SUCCESS,
	))
	kb.pipelinePoolIndex[info] = len(kb.pipelinePool)
	kb.pipelinePool = append(kb.pipelinePool, handle)
	return handle
}

type ipRenderFramebufferInfo struct {
	inputAttachmentImage  VkImage
	inputAttachmentAspect VkImageAspectFlagBits
	outputImage           VkImage
	outputAspect          VkImageAspectFlagBits
	layer                 uint32
	level                 uint32
	width                 uint32
	height                uint32
}

func (kb *ipRenderKitBuilder) getOrCreateFramebuffer(sb *stateBuilder, info ipRenderFramebufferInfo) VkFramebuffer {
	if i, ok := kb.framebufferPoolIndex[info]; ok {
		return kb.framebufferPool[i]
	}
	views := []VkImageView{
		kb.imageViewPool.getOrCreateImageView(sb, kb.nm, ipImageViewInfo{
			image:  info.inputAttachmentImage,
			aspect: VkImageAspectFlagBits_VK_IMAGE_ASPECT_COLOR_BIT,
			layer:  info.layer,
			level:  info.level,
		}),
		kb.imageViewPool.getOrCreateImageView(sb, kb.nm, ipImageViewInfo{
			image:  info.outputImage,
			aspect: info.outputAspect,
			layer:  info.layer,
			level:  info.level,
		}),
	}
	renderPass := kb.getOrCreateRenderPass(sb, ipRenderRenderPassInfo{
		inputAttachmentFormat: GetState(sb.newState).Images().Get(info.inputAttachmentImage).Info().Fmt(),
		outputAspect:          info.outputAspect,
		outputFormat:          GetState(sb.newState).Images().Get(info.outputImage).Info().Fmt(),
	})
	createInfo := NewVkFramebufferCreateInfo(
		VkStructureType_VK_STRUCTURE_TYPE_FRAMEBUFFER_CREATE_INFO, // sType
		0,                  // pNext
		0,                  // flags
		renderPass,         // renderPass
		uint32(len(views)), // attachmentCount
		NewVkImageView??????(sb.MustAllocReadData(views).Ptr()), // pAttachments
		info.width,  // width
		info.height, // height
		1,           // layers
	)
	handle := VkFramebuffer(newUnusedID(true, func(x uint64) bool {
		return GetState(sb.newState).Framebuffers().Contains(VkFramebuffer(x)) ||
			GetState(sb.oldState).Framebuffers().Contains(VkFramebuffer(x))
	}))
	sb.write(sb.cb.VkCreateFramebuffer(
		kb.dev,
		NewVkFramebufferCreateInfo??????(sb.MustAllocReadData(createInfo).Ptr()),
		memory.Nullptr,
		sb.MustAllocWriteData(handle).Ptr(),
		VkResult_VK_SUCCESS,
	))

	kb.framebufferPoolIndex[info] = len(kb.framebufferPool)
	kb.framebufferPool = append(kb.framebufferPool, handle)
	return handle
}
