package main

import (
	"fmt"
	"strings"

	"github.com/lxc/lxd"
	"github.com/lxc/lxd/shared"
	"github.com/lxc/lxd/shared/gnuflag"
	"github.com/lxc/lxd/shared/i18n"
)

type copyCmd struct {
	profArgs profileList
	confArgs configList
	ephem    bool
}

func (c *copyCmd) showByDefault() bool {
	return true
}

var usePush bool

func (c *copyCmd) usage() string {
	return i18n.G(
		`Copy containers within or in between lxd instances.

lxc copy [remote:]<source container> [[remote:]<destination container>] [--ephemeral|e] [--push] [--profile|-p <profile>...] [--config|-c <key=value>...]`)
}

func (c *copyCmd) flags() {
	gnuflag.Var(&c.confArgs, "config", i18n.G("Config key/value to apply to the new container"))
	gnuflag.Var(&c.confArgs, "c", i18n.G("Config key/value to apply to the new container"))
	gnuflag.Var(&c.profArgs, "profile", i18n.G("Profile to apply to the new container"))
	gnuflag.Var(&c.profArgs, "p", i18n.G("Profile to apply to the new container"))
	gnuflag.BoolVar(&c.ephem, "ephemeral", false, i18n.G("Ephemeral container"))
	gnuflag.BoolVar(&c.ephem, "e", false, i18n.G("Ephemeral container"))
	gnuflag.BoolVar(&usePush, "push", false, i18n.G("Use push mode"))
}

func (c *copyCmd) copyContainer(config *lxd.Config, sourceResource string, destResource string, keepVolatile bool, ephemeral int) error {
	sourceRemote, sourceName := config.ParseRemoteAndContainer(sourceResource)
	destRemote, destName := config.ParseRemoteAndContainer(destResource)

	shared.LogWarnf("Use push mode: %t\n", usePush)

	if sourceRemote != "" {
		shared.LogWarnf("sourceRemote: %s\n", sourceRemote)
	} else {
		shared.LogWarnf("sourceRemote: %s\n", "(nil)")
	}

	if destRemote != "" {
		shared.LogWarnf("destRemote: %s\n", destRemote)
	} else {
		shared.LogWarnf("destRemote: %s\n", "(nil)")
	}

	if sourceName == "" {
		return fmt.Errorf(i18n.G("you must specify a source container name"))
	}

	if destName == "" && destResource != "" {
		destName = sourceName
	}

	source, err := lxd.NewClient(config, sourceRemote)
	if err != nil {
		return err
	}

	var status struct {
		Architecture string
		Devices      shared.Devices
		Config       map[string]string
		Profiles     []string
	}

	// TODO: presumably we want to do this for copying snapshots too? We
	// need to think a bit more about how we track the baseImage in the
	// face of LVM and snapshots in general; this will probably make more
	// sense once that work is done.
	baseImage := ""

	if !shared.IsSnapshot(sourceName) {
		result, err := source.ContainerInfo(sourceName)
		if err != nil {
			return err
		}

		status.Architecture = result.Architecture
		status.Devices = result.Devices
		status.Config = result.Config
		status.Profiles = result.Profiles

	} else {
		result, err := source.SnapshotInfo(sourceName)
		if err != nil {
			return err
		}

		status.Architecture = result.Architecture
		status.Devices = result.Devices
		status.Config = result.Config
		status.Profiles = result.Profiles
	}

	if c.profArgs != nil {
		status.Profiles = append(status.Profiles, c.profArgs...)
	}

	if configMap != nil {
		for key, value := range configMap {
			status.Config[key] = value
		}
	}

	baseImage = status.Config["volatile.base_image"]

	if !keepVolatile {
		for k := range status.Config {
			if strings.HasPrefix(k, "volatile") {
				delete(status.Config, k)
			}
		}
	}

	// Do a local copy if the remotes are the same, otherwise do a migration
	if sourceRemote == destRemote {
		if sourceName == destName {
			return fmt.Errorf(i18n.G("can't copy to the same container name"))
		}

		cp, err := source.LocalCopy(sourceName, destName, status.Config, status.Profiles, ephemeral == 1)
		if err != nil {
			return err
		}

		err = source.WaitForSuccess(cp.Operation)
		if err != nil {
			return err
		}

		if destResource == "" {
			op, err := cp.MetadataAsOperation()
			if err != nil {
				return fmt.Errorf(i18n.G("didn't get any affected image, container or snapshot from server"))
			}

			containers, ok := op.Resources["containers"]
			if !ok || len(containers) == 0 {
				return fmt.Errorf(i18n.G("didn't get any affected image, container or snapshot from server"))
			}

			fields := strings.Split(containers[0], "/")
			fmt.Printf(i18n.G("Container name is: %s")+"\n", fields[len(fields)-1])
		}

		return nil
	}

	dest, err := lxd.NewClient(config, destRemote)
	if err != nil {
		return err
	}

	sourceProfs := shared.NewStringSet(status.Profiles)
	destProfs, err := dest.ListProfiles()
	if err != nil {
		return err
	}

	if !sourceProfs.IsSubset(shared.NewStringSet(destProfs)) {
		return fmt.Errorf(i18n.G("not all the profiles from the source exist on the target"))
	}

	if ephemeral == -1 {
		ct, err := source.ContainerInfo(sourceName)
		if err != nil {
			return err
		}

		if ct.Ephemeral {
			ephemeral = 1
		} else {
			ephemeral = 0
		}
	}

        // PULL MODE: We only need one set of websockets + secrets.
	sourceWSResponse, err := source.GetMigrationSourceWS(sourceName, usePush)
	if err != nil {
		return err
	}

	sourceSecrets := map[string]string{}

	op, err := sourceWSResponse.MetadataAsOperation()
	if err != nil {
		return err
	}

	for k, v := range *op.Metadata {
		sourceSecrets[k] = v.(string)
	}
        fmt.Printf("%v\n", sourceSecrets)

	sourceAddresses, err := source.Addresses()
	if err != nil {
		return err
	}

        // PUSH MODE: We need a second set of websockets + secrets.
        destWSResponse, err := dest.GetMigrationSourceWS(destName, usePush)
        // destWSResponse, err := dest.GetMigrationSinkWS(destName, "", source.Certificate, sourceSecrets, status.Architecture, status.Config, status.Devices, status.Profiles, baseImage, ephemeral == 1, usePush)
	if err != nil {
                shared.LogWarnf("AAAAAAAAAAAAAAAAAAAAAAAAAAA")
		return err
	}

	destSecrets := map[string]string{}

	op, err = destWSResponse.MetadataAsOperation()
	if err != nil {
		return err
	}

	for k, v := range *op.Metadata {
		destSecrets[k] = v.(string)
	}
        fmt.Printf("%v\n", destSecrets)

	destAddresses, err := dest.Addresses()
	if err != nil {
		return err
	}
        if destAddresses == nil {
        }

	/* Since we're trying a bunch of different network ports that
	 * may be invalid, we can get "bad handshake" errors when the
	 * websocket code tries to connect. If the first error is a
	 * real error, but the subsequent errors are only network
	 * errors, we should try to report the first real error. Of
	 * course, if all the errors are websocket errors, let's just
	 * report that.
	 */
	for _, addr := range sourceAddresses {
		var migration *lxd.Response

		sourceWSUrl := "https://" + addr + sourceWSResponse.Operation
		migration, err = dest.MigrateFrom(destName, sourceWSUrl, source.Certificate, sourceSecrets, status.Architecture, status.Config, status.Devices, status.Profiles, baseImage, ephemeral == 1, usePush)
		if err != nil {
			continue
		}

		if err = dest.WaitForSuccess(migration.Operation); err != nil {
			return err
		}

		if destResource == "" {
			op, err := migration.MetadataAsOperation()
			if err != nil {
				return fmt.Errorf(i18n.G("didn't get any affected image, container or snapshot from server"))
			}

			containers, ok := op.Resources["containers"]
			if !ok || len(containers) == 0 {
				return fmt.Errorf(i18n.G("didn't get any affected image, container or snapshot from server"))
			}

			fields := strings.Split(containers[0], "/")
			fmt.Printf(i18n.G("Container name is: %s")+"\n", fields[len(fields)-1])
		}

		return nil
	}

	return err
}

func (c *copyCmd) run(config *lxd.Config, args []string) error {
	if len(args) < 1 {
		return errArgs
	}

	ephem := 0
	if c.ephem {
		ephem = 1
	}

	if len(args) < 2 {
		return c.copyContainer(config, args[0], "", false, ephem)
	}

	return c.copyContainer(config, args[0], args[1], false, ephem)
}
