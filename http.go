package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/antongulenko/golib"
	"github.com/gin-gonic/gin"
	"github.com/kballard/go-shellquote"
	log "github.com/sirupsen/logrus"
)

const (
	DefaultNewPipelineDelay    = 200 * time.Millisecond
	NewPipelineDelayQuery      = "delay"
	NewPipelineParametersQuery = "params"
)

func (engine *SubprocessEngine) ServeHttp(endpoint string) error {
	g := golib.NewGinEngine()
	g.GET("/ping", engine.servePing)
	g.GET("/info", engine.serveInfo)
	g.GET("/capabilities", engine.serveCapabilities)
	g.GET("/pipelines", engine.servePipelines)
	g.GET("/running", engine.serveRunningPipelines)
	g.POST("/pipeline", engine.serveNewPipeline)
	g.GET("/pipeline/:id", engine.serveGetPipeline)
	g.GET("/pipeline/:id/out", engine.serveGetPipelineOutput)
	g.DELETE("/pipeline/:id", engine.serveKillPipeline)
	return g.Run(endpoint)
}

func (engine *SubprocessEngine) replyString(c *gin.Context, code int, format string, args ...interface{}) {
	c.Status(code)
	c.Writer.WriteString(fmt.Sprintf(format+"\n", args...))
}

func (engine *SubprocessEngine) servePing(c *gin.Context) {
	engine.replyString(c, http.StatusOK, "pong")
}

func (engine *SubprocessEngine) serveInfo(c *gin.Context) {
	c.JSON(http.StatusOK, engine.getInfo())
}

func (engine *SubprocessEngine) serveCapabilities(c *gin.Context) {
	c.JSON(http.StatusOK, engine.capabilities)
}

func (engine *SubprocessEngine) serveFilteredPipelineIds(c *gin.Context, accept func(*RunningPipeline) bool) {
	engine.pipelinesLock.Lock()
	response := make([]int, 0, len(engine.pipelines))
	for _, pipe := range engine.pipelines {
		if accept(pipe) {
			response = append(response, pipe.Id)
		}
	}
	engine.pipelinesLock.Unlock()
	sort.Ints(response)
	c.JSON(http.StatusOK, response)
}

func (engine *SubprocessEngine) servePipelines(c *gin.Context) {
	engine.serveFilteredPipelineIds(c, func(*RunningPipeline) bool {
		return true
	})
}

func (engine *SubprocessEngine) serveRunningPipelines(c *gin.Context) {
	engine.serveFilteredPipelineIds(c, func(pipe *RunningPipeline) bool {
		return pipe.Status == StatusRunning
	})
}

func (engine *SubprocessEngine) pipelineResponse(pipe *RunningPipeline) interface{} {
	// TODO maybe don't serve the entire internal struct?
	return pipe
}

func (engine *SubprocessEngine) serveNewPipeline(c *gin.Context) {
	defer func() {
		err := c.Request.Body.Close()
		if err != nil {
			log.Warnln("Error closing POST request body:", err)
		}
	}()

	script, err := ioutil.ReadAll(c.Request.Body)
	if err != nil {
		engine.replyString(c, http.StatusInternalServerError, "Failed to read request body: "+err.Error())
		return
	}

	if len(script) == 0 {
		engine.replyString(c, http.StatusBadRequest, "Provide the Bitflow script for the new pipeline as the POST request body.")
		return
	}

	delay := DefaultNewPipelineDelay
	if delayStr := c.Query(NewPipelineDelayQuery); delayStr != "" {
		parsedDelay, err := time.ParseDuration(delayStr)
		if err != nil {
			engine.replyString(c, http.StatusBadRequest, "The parameter '%v' could not be parsed to a duration: %v. Example format: 500ms",
				NewPipelineDelayQuery, err)
			return
		}
		delay = parsedDelay
	}

	var extraParams []string
	if extraParamString := c.Query(NewPipelineParametersQuery); extraParamString != "" {
		extraParamsSplit, err := shellquote.Split(extraParamString)
		if err != nil {
			engine.replyString(c, http.StatusBadRequest, "The parameter '%v' could not be split into shell parameters: %v. The quote syntax must follow /bin/sh.",
				NewPipelineParametersQuery, err)
			return
		}
		extraParams = extraParamsSplit
	}

	pipeline, err := engine.NewPipeline(string(script), delay, extraParams)
	if err != nil {
		engine.replyString(c, http.StatusPreconditionFailed, "Error starting pipeline %v: %v", pipeline.Id, err.Error())
	} else {
		c.JSON(http.StatusCreated, engine.pipelineResponse(pipeline))
	}
}

func (engine *SubprocessEngine) serveGetPipeline(c *gin.Context) {
	pipe := engine.getPipeline(c)
	if pipe != nil {
		c.JSON(http.StatusOK, engine.pipelineResponse(pipe))
	}
}

func (engine *SubprocessEngine) serveGetPipelineOutput(c *gin.Context) {
	pipe := engine.getPipeline(c)
	if pipe != nil {
		out, err := pipe.GetOutput()
		if err == nil {
			c.Status(http.StatusOK)
			c.Writer.Write(out)
		} else {
			engine.replyString(c, http.StatusInternalServerError, "Error obtaining output of pipeline %v", pipe.Id)
		}
	}
}

func (engine *SubprocessEngine) serveKillPipeline(c *gin.Context) {
	pipe := engine.getPipeline(c)
	if pipe != nil {
		err := pipe.Kill()
		if err != nil {
			engine.replyString(c, http.StatusInternalServerError, "Error killing pipeline %v: %v", pipe.Id, err)
		} else {
			c.JSON(http.StatusOK, pipe)
		}
	}
}

func (engine *SubprocessEngine) getPipeline(c *gin.Context) *RunningPipeline {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		engine.replyString(c, http.StatusBadRequest, "Failed to parse parameter '%v' to int: %v", idStr, err)
	}

	engine.pipelinesLock.Lock()
	pipeline, exists := engine.pipelines[id]
	engine.pipelinesLock.Unlock()
	if !exists {
		engine.replyString(c, http.StatusNotFound, "Pipeline does not exist: "+idStr)
	}
	return pipeline
}
