package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/docker/distribution/manifest/schema2"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/integration-cli/cli"
	"github.com/docker/docker/integration-cli/cli/build"
	"github.com/docker/docker/internal/lazyregexp"
	"github.com/opencontainers/go-digest"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/skip"
)

const (
	remoteRepoName = "dockercli/busybox-by-dgst"
	repoName       = privateRegistryURL + "/" + remoteRepoName
)

var (
	pushDigestRegex = lazyregexp.New(`[\S]+: digest: ([\S]+) size: [0-9]+`)
	digestRegex     = lazyregexp.New(`Digest: ([\S]+)`)
)

func setupImage(c *testing.T) (digest.Digest, error) {
	return setupImageWithTag(c, "latest")
}

func setupImageWithTag(c *testing.T, tag string) (digest.Digest, error) {
	const containerName = "busyboxbydigest"

	// new file is committed because this layer is used for detecting malicious
	// changes. if this was committed as empty layer it would be skipped on pull
	// and malicious changes would never be detected.
	cli.DockerCmd(c, "run", "-e", "digest=1", "--name", containerName, "busybox", "touch", "anewfile")

	// tag the image to upload it to the private registry
	repoAndTag := repoName + ":" + tag
	cli.DockerCmd(c, "commit", containerName, repoAndTag)

	// delete the container as we don't need it any more
	cli.DockerCmd(c, "rm", "-fv", containerName)

	// push the image
	out := cli.DockerCmd(c, "push", repoAndTag).Combined()

	// delete our local repo that we previously tagged
	cli.DockerCmd(c, "rmi", repoAndTag)

	matches := pushDigestRegex.FindStringSubmatch(out)
	assert.Equal(c, len(matches), 2, "unable to parse digest from push output: %s", out)
	pushDigest := matches[1]

	return digest.Digest(pushDigest), nil
}

func (s *DockerRegistrySuite) TestPullByTagDisplaysDigest(c *testing.T) {
	testRequires(c, DaemonIsLinux)
	pushDigest, err := setupImage(c)
	assert.NilError(c, err, "error setting up image")

	// pull from the registry using the tag
	out := cli.DockerCmd(c, "pull", repoName).Combined()

	// the pull output includes "Digest: <digest>", so find that
	matches := digestRegex.FindStringSubmatch(out)
	assert.Equal(c, len(matches), 2, "unable to parse digest from push output: %s", out)
	pullDigest := matches[1]

	// make sure the pushed and pull digests match
	assert.Equal(c, pushDigest.String(), pullDigest)
}

func (s *DockerRegistrySuite) TestPullByDigest(c *testing.T) {
	testRequires(c, DaemonIsLinux)
	pushDigest, err := setupImage(c)
	assert.NilError(c, err, "error setting up image")

	// pull from the registry using the <name>@<digest> reference
	imageReference := fmt.Sprintf("%s@%s", repoName, pushDigest)
	out := cli.DockerCmd(c, "pull", imageReference).Combined()

	// the pull output includes "Digest: <digest>", so find that
	matches := digestRegex.FindStringSubmatch(out)
	assert.Equal(c, len(matches), 2, "unable to parse digest from push output: %s", out)
	pullDigest := matches[1]

	// make sure the pushed and pull digests match
	assert.Equal(c, pushDigest.String(), pullDigest)
}

func (s *DockerRegistrySuite) TestPullByDigestNoFallback(c *testing.T) {
	testRequires(c, DaemonIsLinux)
	// pull from the registry using the <name>@<digest> reference
	imageReference := fmt.Sprintf("%s@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", repoName)
	out, _, err := dockerCmdWithError("pull", imageReference)
	assert.Assert(c, err != nil, "expected non-zero exit status and correct error message when pulling non-existing image")

	expectedMsg := fmt.Sprintf("manifest for %s not found", imageReference)
	if testEnv.UsingSnapshotter() {
		expectedMsg = fmt.Sprintf("%s: not found", imageReference)
	}

	assert.Check(c, is.Contains(out, expectedMsg), "expected non-zero exit status and correct error message when pulling non-existing image")
}

func (s *DockerRegistrySuite) TestCreateByDigest(c *testing.T) {
	pushDigest, err := setupImage(c)
	assert.NilError(c, err, "error setting up image")

	imageReference := fmt.Sprintf("%s@%s", repoName, pushDigest)

	const containerName = "createByDigest"
	cli.DockerCmd(c, "create", "--name", containerName, imageReference)

	res := inspectField(c, containerName, "Config.Image")
	assert.Equal(c, res, imageReference)
}

func (s *DockerRegistrySuite) TestRunByDigest(c *testing.T) {
	pushDigest, err := setupImage(c)
	assert.NilError(c, err)

	imageReference := fmt.Sprintf("%s@%s", repoName, pushDigest)

	const containerName = "runByDigest"
	out := cli.DockerCmd(c, "run", "--name", containerName, imageReference, "sh", "-c", "echo found=$digest").Combined()

	foundRegex := regexp.MustCompile("found=([^\n]+)")
	matches := foundRegex.FindStringSubmatch(out)
	assert.Equal(c, len(matches), 2, fmt.Sprintf("unable to parse digest from pull output: %s", out))
	assert.Equal(c, matches[1], "1", fmt.Sprintf("Expected %q, got %q", "1", matches[1]))

	res := inspectField(c, containerName, "Config.Image")
	assert.Equal(c, res, imageReference)
}

func (s *DockerRegistrySuite) TestRemoveImageByDigest(c *testing.T) {
	imgDigest, err := setupImage(c)
	assert.NilError(c, err, "error setting up image")

	imageReference := fmt.Sprintf("%s@%s", repoName, imgDigest)

	// pull from the registry using the <name>@<digest> reference
	cli.DockerCmd(c, "pull", imageReference)

	// make sure inspect runs ok
	inspectField(c, imageReference, "Id")

	// do the delete
	err = deleteImages(imageReference)
	assert.NilError(c, err, "unexpected error deleting image")

	// try to inspect again - it should error this time
	_, err = inspectFieldWithError(imageReference, "Id")
	// unexpected nil err trying to inspect what should be a non-existent image
	assert.ErrorContains(c, err, "No such object")
}

func (s *DockerRegistrySuite) TestBuildByDigest(c *testing.T) {
	skip.If(c, testEnv.UsingSnapshotter(), "Config.Image is not created with containerd, buildkit doesn't set it either")
	imgDigest, err := setupImage(c)
	assert.NilError(c, err, "error setting up image")

	imageReference := fmt.Sprintf("%s@%s", repoName, imgDigest)

	// pull from the registry using the <name>@<digest> reference
	cli.DockerCmd(c, "pull", imageReference)

	// get the image id
	imageID := inspectField(c, imageReference, "Id")

	// do the build
	const name = "buildbydigest"
	buildImageSuccessfully(c, name, build.WithDockerfile(fmt.Sprintf(
		`FROM %s
     CMD ["/bin/echo", "Hello World"]`, imageReference)))
	assert.NilError(c, err)

	// get the build's image id
	res := inspectField(c, name, "Config.Image")
	// make sure they match
	assert.Equal(c, res, imageID)
}

func (s *DockerRegistrySuite) TestTagByDigest(c *testing.T) {
	imgDigest, err := setupImage(c)
	assert.NilError(c, err, "error setting up image")

	imageReference := fmt.Sprintf("%s@%s", repoName, imgDigest)

	// pull from the registry using the <name>@<digest> reference
	cli.DockerCmd(c, "pull", imageReference)

	// tag it
	const tag = "tagbydigest"
	cli.DockerCmd(c, "tag", imageReference, tag)

	expectedID := inspectField(c, imageReference, "Id")

	tagID := inspectField(c, tag, "Id")
	assert.Equal(c, tagID, expectedID)
}

func (s *DockerRegistrySuite) TestListImagesWithoutDigests(c *testing.T) {
	imgDigest, err := setupImage(c)
	assert.NilError(c, err, "error setting up image")

	imageReference := fmt.Sprintf("%s@%s", repoName, imgDigest)

	// pull from the registry using the <name>@<digest> reference
	cli.DockerCmd(c, "pull", imageReference)

	out := cli.DockerCmd(c, "images").Stdout()
	assert.Assert(c, !strings.Contains(out, "DIGEST"), "list output should not have contained DIGEST header")
}

func (s *DockerRegistrySuite) TestListImagesWithDigests(c *testing.T) {
	// setup image1
	digest1, err := setupImageWithTag(c, "tag1")
	assert.NilError(c, err, "error setting up image")
	imageReference1 := fmt.Sprintf("%s@%s", repoName, digest1)
	c.Logf("imageReference1 = %s", imageReference1)

	// pull image1 by digest
	cli.DockerCmd(c, "pull", imageReference1)

	// list images
	out := cli.DockerCmd(c, "images", "--digests").Combined()

	// make sure repo shown, tag=<none>, digest = $digest1
	re1 := regexp.MustCompile(`\s*` + repoName + `\s*<none>\s*` + digest1.String() + `\s`)
	assert.Assert(c, re1.MatchString(out), "expected %q: %s", re1.String(), out)
	// setup image2
	digest2, err := setupImageWithTag(c, "tag2")
	assert.NilError(c, err, "error setting up image")
	imageReference2 := fmt.Sprintf("%s@%s", repoName, digest2)
	c.Logf("imageReference2 = %s", imageReference2)

	// pull image1 by digest
	cli.DockerCmd(c, "pull", imageReference1)

	// pull image2 by digest
	cli.DockerCmd(c, "pull", imageReference2)

	// list images
	out = cli.DockerCmd(c, "images", "--digests").Stdout()

	// make sure repo shown, tag=<none>, digest = $digest1
	assert.Assert(c, re1.MatchString(out), "expected %q: %s", re1.String(), out)

	// make sure repo shown, tag=<none>, digest = $digest2
	re2 := regexp.MustCompile(`\s*` + repoName + `\s*<none>\s*` + digest2.String() + `\s`)
	assert.Assert(c, re2.MatchString(out), "expected %q: %s", re2.String(), out)

	// pull tag1
	cli.DockerCmd(c, "pull", repoName+":tag1")

	// list images
	out = cli.DockerCmd(c, "images", "--digests").Stdout()

	// make sure image 1 has repo, tag, <none> AND repo, <none>, digest
	reWithDigest1 := regexp.MustCompile(`\s*` + repoName + `\s*tag1\s*` + digest1.String() + `\s`)
	assert.Assert(c, reWithDigest1.MatchString(out), "expected %q: %s", reWithDigest1.String(), out)
	// make sure image 2 has repo, <none>, digest
	assert.Assert(c, re2.MatchString(out), "expected %q: %s", re2.String(), out)

	// pull tag 2
	cli.DockerCmd(c, "pull", repoName+":tag2")

	// list images
	out = cli.DockerCmd(c, "images", "--digests").Stdout()

	// make sure image 1 has repo, tag, digest
	assert.Assert(c, reWithDigest1.MatchString(out), "expected %q: %s", reWithDigest1.String(), out)

	// make sure image 2 has repo, tag, digest
	reWithDigest2 := regexp.MustCompile(`\s*` + repoName + `\s*tag2\s*` + digest2.String() + `\s`)
	assert.Assert(c, reWithDigest2.MatchString(out), "expected %q: %s", reWithDigest2.String(), out)

	// list images
	out = cli.DockerCmd(c, "images", "--digests").Stdout()

	// make sure image 1 has repo, tag, digest
	assert.Assert(c, reWithDigest1.MatchString(out), "expected %q: %s", reWithDigest1.String(), out)
	// make sure image 2 has repo, tag, digest
	assert.Assert(c, reWithDigest2.MatchString(out), "expected %q: %s", reWithDigest2.String(), out)
	// We always have a digest when using containerd to store images
	if !testEnv.UsingSnapshotter() {
		// make sure busybox has tag, but not digest
		busyboxRe := regexp.MustCompile(`\s*busybox\s*latest\s*<none>\s`)
		assert.Assert(c, busyboxRe.MatchString(out), "expected %q: %s", busyboxRe.String(), out)
	}
}

func (s *DockerRegistrySuite) TestListDanglingImagesWithDigests(c *testing.T) {
	// See https://github.com/moby/moby/pull/46856
	skip.If(c, testEnv.UsingSnapshotter(), "dangling=true filter behaves a bit differently with c8d")

	// setup image1
	digest1, err := setupImageWithTag(c, "dangle1")
	assert.NilError(c, err, "error setting up image")
	imageReference1 := fmt.Sprintf("%s@%s", repoName, digest1)
	c.Logf("imageReference1 = %s", imageReference1)

	// pull image1 by digest
	cli.DockerCmd(c, "pull", imageReference1)

	// list images
	out := cli.DockerCmd(c, "images", "--digests").Stdout()

	// make sure repo shown, tag=<none>, digest = $digest1
	re1 := regexp.MustCompile(`\s*` + repoName + `\s*<none>\s*` + digest1.String() + `\s`)
	assert.Assert(c, re1.MatchString(out), "expected %q: %s", re1.String(), out)
	// setup image2
	digest2, err := setupImageWithTag(c, "dangle2")
	// error setting up image
	assert.NilError(c, err)
	imageReference2 := fmt.Sprintf("%s@%s", repoName, digest2)
	c.Logf("imageReference2 = %s", imageReference2)

	// pull image1 by digest
	cli.DockerCmd(c, "pull", imageReference1)

	// pull image2 by digest
	cli.DockerCmd(c, "pull", imageReference2)

	// list images
	out = cli.DockerCmd(c, "images", "--digests", "--filter=dangling=true").Stdout()

	// make sure repo shown, tag=<none>, digest = $digest1
	assert.Assert(c, re1.MatchString(out), "expected %q: %s", re1.String(), out)

	// make sure repo shown, tag=<none>, digest = $digest2
	re2 := regexp.MustCompile(`\s*` + repoName + `\s*<none>\s*` + digest2.String() + `\s`)
	assert.Assert(c, re2.MatchString(out), "expected %q: %s", re2.String(), out)

	// pull dangle1 tag
	cli.DockerCmd(c, "pull", repoName+":dangle1")

	// list images
	out = cli.DockerCmd(c, "images", "--digests", "--filter=dangling=true").Stdout()

	// make sure image 1 has repo, tag, <none> AND repo, <none>, digest
	reWithDigest1 := regexp.MustCompile(`\s*` + repoName + `\s*dangle1\s*` + digest1.String() + `\s`)
	assert.Assert(c, !reWithDigest1.MatchString(out), "unexpected %q: %s", reWithDigest1.String(), out)
	// make sure image 2 has repo, <none>, digest
	assert.Assert(c, re2.MatchString(out), "expected %q: %s", re2.String(), out)

	// pull dangle2 tag
	cli.DockerCmd(c, "pull", repoName+":dangle2")

	// list images, show tagged images
	out = cli.DockerCmd(c, "images", "--digests").Stdout()

	// make sure image 1 has repo, tag, digest
	assert.Assert(c, reWithDigest1.MatchString(out), "expected %q: %s", reWithDigest1.String(), out)

	// make sure image 2 has repo, tag, digest
	reWithDigest2 := regexp.MustCompile(`\s*` + repoName + `\s*dangle2\s*` + digest2.String() + `\s`)
	assert.Assert(c, reWithDigest2.MatchString(out), "expected %q: %s", reWithDigest2.String(), out)

	// list images, no longer dangling, should not match
	out = cli.DockerCmd(c, "images", "--digests", "--filter=dangling=true").Stdout()

	// make sure image 1 has repo, tag, digest
	assert.Assert(c, !reWithDigest1.MatchString(out), "unexpected %q: %s", reWithDigest1.String(), out)
	// make sure image 2 has repo, tag, digest
	assert.Assert(c, !reWithDigest2.MatchString(out), "unexpected %q: %s", reWithDigest2.String(), out)
}

func (s *DockerRegistrySuite) TestInspectImageWithDigests(c *testing.T) {
	imgDigest, err := setupImage(c)
	assert.Assert(c, err == nil, "error setting up image")

	imageReference := fmt.Sprintf("%s@%s", repoName, imgDigest)

	// pull from the registry using the <name>@<digest> reference
	cli.DockerCmd(c, "pull", imageReference)

	out := cli.DockerCmd(c, "inspect", imageReference).Stdout()

	var imageJSON []image.InspectResponse
	err = json.Unmarshal([]byte(out), &imageJSON)
	assert.NilError(c, err)
	assert.Equal(c, len(imageJSON), 1)
	assert.Equal(c, len(imageJSON[0].RepoDigests), 1)
	assert.Check(c, is.Contains(imageJSON[0].RepoDigests, imageReference))
}

func (s *DockerRegistrySuite) TestPsListContainersFilterAncestorImageByDigest(c *testing.T) {
	existingContainers := ExistingContainerIDs(c)

	imgDigest, err := setupImage(c)
	assert.NilError(c, err, "error setting up image")

	imageReference := fmt.Sprintf("%s@%s", repoName, imgDigest)

	// pull from the registry using the <name>@<digest> reference
	cli.DockerCmd(c, "pull", imageReference)

	// build an image from it
	const imageName1 = "images_ps_filter_test"
	buildImageSuccessfully(c, imageName1, build.WithDockerfile(fmt.Sprintf(
		`FROM %s
		 LABEL match me 1`, imageReference)))

	// run a container based on that
	cli.DockerCmd(c, "run", "--name=test1", imageReference, "echo", "hello")
	expectedID := getIDByName(c, "test1")

	// run a container based on the a descendant of that too
	cli.DockerCmd(c, "run", "--name=test2", imageName1, "echo", "hello")
	expectedID1 := getIDByName(c, "test2")

	expectedIDs := []string{expectedID, expectedID1}

	// Invalid imageReference
	out := cli.DockerCmd(c, "ps", "-a", "-q", "--no-trunc", fmt.Sprintf("--filter=ancestor=busybox@%s", imgDigest)).Stdout()
	assert.Equal(c, strings.TrimSpace(out), "", "Filter container for ancestor filter should be empty")

	// Valid imageReference
	out = cli.DockerCmd(c, "ps", "-a", "-q", "--no-trunc", "--filter=ancestor="+imageReference).Stdout()
	checkPsAncestorFilterOutput(c, RemoveOutputForExistingElements(out, existingContainers), imageReference, expectedIDs)
}

func (s *DockerRegistrySuite) TestDeleteImageByIDOnlyPulledByDigest(c *testing.T) {
	pushDigest, err := setupImage(c)
	assert.NilError(c, err, "error setting up image")

	// pull from the registry using the <name>@<digest> reference
	imageReference := fmt.Sprintf("%s@%s", repoName, pushDigest)
	cli.DockerCmd(c, "pull", imageReference)
	// just in case...

	cli.DockerCmd(c, "tag", imageReference, repoName+":sometag")

	imageID := inspectField(c, imageReference, "Id")

	cli.DockerCmd(c, "rmi", imageID)

	_, err = inspectFieldWithError(imageID, "Id")
	assert.ErrorContains(c, err, "", "image should have been deleted")
}

func (s *DockerRegistrySuite) TestDeleteImageWithDigestAndTag(c *testing.T) {
	pushDigest, err := setupImage(c)
	assert.NilError(c, err, "error setting up image")

	// pull from the registry using the <name>@<digest> reference
	imageReference := fmt.Sprintf("%s@%s", repoName, pushDigest)
	cli.DockerCmd(c, "pull", imageReference)

	imageID := inspectField(c, imageReference, "Id")

	const repoTag = repoName + ":sometag"
	const repoTag2 = repoName + ":othertag"
	cli.DockerCmd(c, "tag", imageReference, repoTag)
	cli.DockerCmd(c, "tag", imageReference, repoTag2)

	cli.DockerCmd(c, "rmi", repoTag2)

	// rmi should have deleted only repoTag2, because there's another tag
	inspectField(c, repoTag, "Id")

	cli.DockerCmd(c, "rmi", repoTag)

	// rmi should have deleted the tag, the digest reference, and the image itself
	_, err = inspectFieldWithError(imageID, "Id")
	assert.ErrorContains(c, err, "", "image should have been deleted")
}

func (s *DockerRegistrySuite) TestDeleteImageWithDigestAndMultiRepoTag(c *testing.T) {
	pushDigest, err := setupImage(c)
	assert.NilError(c, err, "error setting up image")

	repo2 := fmt.Sprintf("%s/%s", repoName, "repo2")

	// pull from the registry using the <name>@<digest> reference
	imageReference := fmt.Sprintf("%s@%s", repoName, pushDigest)
	cli.DockerCmd(c, "pull", imageReference)

	imageID := inspectField(c, imageReference, "Id")

	repoTag := repoName + ":sometag"
	repoTag2 := repo2 + ":othertag"
	cli.DockerCmd(c, "tag", imageReference, repoTag)
	cli.DockerCmd(c, "tag", imageReference, repoTag2)

	cli.DockerCmd(c, "rmi", repoTag)

	// rmi should have deleted repoTag and image reference, but left repoTag2
	inspectField(c, repoTag2, "Id")
	_, err = inspectFieldWithError(imageReference, "Id")
	assert.ErrorContains(c, err, "", "image digest reference should have been removed")

	_, err = inspectFieldWithError(repoTag, "Id")
	assert.ErrorContains(c, err, "", "image tag reference should have been removed")

	cli.DockerCmd(c, "rmi", repoTag2)

	// rmi should have deleted the tag, the digest reference, and the image itself
	_, err = inspectFieldWithError(imageID, "Id")
	assert.ErrorContains(c, err, "", "image should have been deleted")
}

// TestPullFailsWithAlteredManifest tests that a `docker pull` fails when
// we have modified a manifest blob and its digest cannot be verified.
// This is the schema2 version of the test.
func (s *DockerRegistrySuite) TestPullFailsWithAlteredManifest(c *testing.T) {
	testRequires(c, DaemonIsLinux)
	manifestDigest, err := setupImage(c)
	assert.NilError(c, err, "error setting up image")

	// Load the target manifest blob.
	manifestBlob := s.reg.ReadBlobContents(c, manifestDigest)

	var imgManifest schema2.Manifest
	err = json.Unmarshal(manifestBlob, &imgManifest)
	assert.NilError(c, err, "unable to decode image manifest from blob")

	// Change a layer in the manifest.
	imgManifest.Layers[0].Digest = digest.Digest("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	// Move the existing data file aside, so that we can replace it with a
	// malicious blob of data. NOTE: we defer the returned undo func.
	undo := s.reg.TempMoveBlobData(c, manifestDigest)
	defer undo()

	alteredManifestBlob, err := json.MarshalIndent(imgManifest, "", "   ")
	assert.NilError(c, err, "unable to encode altered image manifest to JSON")

	s.reg.WriteBlobContents(c, manifestDigest, alteredManifestBlob)

	// Now try pulling that image by digest. We should get an error about
	// digest verification for the manifest digest.

	// Pull from the registry using the <name>@<digest> reference.
	imageReference := fmt.Sprintf("%s@%s", repoName, manifestDigest)
	out, exitStatus, _ := dockerCmdWithError("pull", imageReference)
	assert.Assert(c, exitStatus != 0)

	if testEnv.UsingSnapshotter() {
		assert.Assert(c, is.Contains(out, "unexpected commit digest"))
		assert.Assert(c, is.Contains(out, "expected "+manifestDigest))
	} else {
		assert.Assert(c, is.Contains(out, fmt.Sprintf("manifest verification failed for digest %s", manifestDigest)))
	}
}

// TestPullFailsWithAlteredLayer tests that a `docker pull` fails when
// we have modified a layer blob and its digest cannot be verified.
// This is the schema2 version of the test.
func (s *DockerRegistrySuite) TestPullFailsWithAlteredLayer(c *testing.T) {
	testRequires(c, DaemonIsLinux)
	skip.If(c, testEnv.UsingSnapshotter(), "Faked layer is already in the content store, so it won't be fetched from the repository at all.")

	manifestDigest, err := setupImage(c)
	assert.NilError(c, err)

	// Load the target manifest blob.
	manifestBlob := s.reg.ReadBlobContents(c, manifestDigest)

	var imgManifest schema2.Manifest
	err = json.Unmarshal(manifestBlob, &imgManifest)
	assert.NilError(c, err)

	// Next, get the digest of one of the layers from the manifest.
	targetLayerDigest := imgManifest.Layers[0].Digest

	// Move the existing data file aside, so that we can replace it with a
	// malicious blob of data. NOTE: we defer the returned undo func.
	undo := s.reg.TempMoveBlobData(c, targetLayerDigest)
	defer undo()

	// Now make a fake data blob in this directory.
	s.reg.WriteBlobContents(c, targetLayerDigest, []byte("This is not the data you are looking for."))

	// Now try pulling that image by digest. We should get an error about
	// digest verification for the target layer digest.

	// Remove distribution cache to force a re-pull of the blobs
	if err := os.RemoveAll(filepath.Join(testEnv.DaemonInfo.DockerRootDir, "image", s.d.StorageDriver(), "distribution")); err != nil {
		c.Fatalf("error clearing distribution cache: %v", err)
	}

	// Pull from the registry using the <name>@<digest> reference.
	imageReference := fmt.Sprintf("%s@%s", repoName, manifestDigest)
	out, exitStatus, _ := dockerCmdWithError("pull", imageReference)
	assert.Assert(c, exitStatus != 0, "expected a non-zero exit status")

	expectedErrorMsg := fmt.Sprintf("filesystem layer verification failed for digest %s", targetLayerDigest)
	assert.Assert(c, strings.Contains(out, expectedErrorMsg), "expected error message in output: %s", out)
}
