package lutron

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/markdaws/gohome"
	"github.com/markdaws/gohome/cmd"
	"github.com/markdaws/gohome/comm"
	"github.com/markdaws/gohome/zone"
)

type importer struct {
	System *gohome.System
}

func (imp *importer) FromString(system *gohome.System, data, modelNumber string) error {

	//TODO: Don't hard code, modify lutron integration report JSON on upload to
	//include this ID
	var smartBridgeProID string = "1"
	var smartBridgeProAddress string = "192.168.0.10:23"

	var configJSON map[string]interface{}
	if err := json.Unmarshal([]byte(data), &configJSON); err != nil {
		return err
	}

	root, ok := configJSON["LIPIdList"].(map[string]interface{})
	if !ok {
		return errors.New("Missing LIPIdList key, or value not a map")
	}
	devices, ok := root["Devices"].([]interface{})
	if !ok {
		return errors.New("Missing Devices key, or value not a map")
	}

	var makeDevice = func(
		modelNumber, name, address string,
		deviceMap map[string]interface{},
		hub *gohome.Device,
		sys *gohome.System,
		stream bool,
		auth *comm.Auth) gohome.Device {

		device, _ := gohome.NewDevice(
			modelNumber,
			address,
			sys.NextGlobalID(),
			name,
			"",
			hub,
			stream,
			nil,
			nil,
			auth)

		for _, buttonMap := range deviceMap["Buttons"].([]interface{}) {
			button := buttonMap.(map[string]interface{})
			btnNumber := strconv.FormatFloat(button["Number"].(float64), 'f', 0, 64)

			var btnName string
			if name, ok := button["Name"]; ok {
				btnName = name.(string)
			} else {
				btnName = "Button " + btnNumber
			}

			b := &gohome.Button{
				Address:     btnNumber,
				ID:          sys.NextGlobalID(),
				Name:        btnName,
				Description: btnName,
				Device:      *device,
			}
			device.Buttons[btnNumber] = b
			system.AddButton(b)
		}

		return *device
	}

	var makeScenes = func(deviceMap map[string]interface{}, sbp gohome.Device) error {
		buttons, ok := deviceMap["Buttons"].([]interface{})
		if !ok {
			return errors.New("Missing Buttons key, or value not array")
		}

		for _, buttonMap := range buttons {
			button, ok := buttonMap.(map[string]interface{})
			if !ok {
				return errors.New("Expected Button elements to be objects")
			}
			if name, ok := button["Name"]; ok && !strings.HasPrefix(name.(string), "Button ") {
				//fmt.Printf("  Scene %d: %s\n", int(button["Number"].(float64)), name)

				var buttonID string = strconv.FormatFloat(button["Number"].(float64), 'f', 0, 64)
				var buttonName = button["Name"].(string)

				var btn = sbp.Buttons[buttonID]
				scene := &gohome.Scene{
					Address:     buttonID,
					Name:        buttonName,
					Description: buttonName,
					Commands: []cmd.Command{
						&cmd.ButtonPress{
							ButtonAddress: btn.Address,
							ButtonID:      btn.ID,
							DeviceName:    sbp.Name,
							DeviceAddress: sbp.Address,
							DeviceID:      sbp.ID,
						},
						&cmd.ButtonRelease{
							ButtonAddress: btn.Address,
							ButtonID:      btn.ID,
							DeviceName:    sbp.Name,
							DeviceAddress: sbp.Address,
							DeviceID:      sbp.ID,
						},
					},
				}
				err := system.AddScene(scene)
				if err != nil {
					fmt.Printf("error adding scene: %s\n", err)
				}
			}
		}

		return nil
	}

	// First need to find the Smart Bridge Pro since it is needed to make scenes and zones
	var sbp *gohome.Device
	for _, deviceMap := range devices {
		device, ok := deviceMap.(map[string]interface{})
		if !ok {
			return errors.New("Expected Devices elements to be objects")
		}

		var deviceID = strconv.FormatFloat(device["ID"].(float64), 'f', 0, 64)
		if deviceID == smartBridgeProID {

			//TODO: Don't hard code address
			dev := makeDevice(
				"l-bdgpro2-wh",
				"Smart Bridge - Hub",
				smartBridgeProAddress,
				device,
				nil,
				system,
				true,
				&comm.Auth{
					Login:    "lutron",
					Password: "integration",
				})
			sbp = &dev

			builder, ok := system.Extensions.CmdBuilders["l-bdgpro2-wh"]
			if !ok {
				//TODO: Err
			}
			sbp.CmdBuilder = builder

			pool, _ := comm.NewConnectionPool(comm.ConnectionPoolConfig{
				Name:           sbp.Name,
				Size:           2,
				ConnectionType: "telnet",
				Address:        sbp.Address,
				TelnetPingCmd:  "",
				TelnetAuth:     &comm.TelnetAuthenticator{*sbp.Auth},
			})
			sbp.Connections = pool

			//TODO: Add event parser

			// TODO: Phantom device still needed?
			// The smart bridge pro controls scenes by having phantom buttons that can be pressed,
			// each button activates a different scene. This means it really has two addresses, the
			// first is the IP address to talk to it, but then it also have the DeviceID which is needed
			// to press the buttons, so here, we make another device and assign the buttons to this
			// new device and use the lutron hub solely for communicating to.
			fmt.Printf("phantom id: %s\n", deviceID)
			sbpSceneDevice := makeDevice("", "Smart Bridge - Phantom Buttons", deviceID, device, sbp, system, false, nil)
			system.AddDevice(sbpSceneDevice)
			sbp.AddDevice(sbpSceneDevice)
			makeScenes(device, sbpSceneDevice)
			break
		}
	}

	if sbp == nil {
		return errors.New("Did not find Smart Bridge Pro with ID:" + smartBridgeProID)
	}
	system.AddDevice(*sbp)

	for _, deviceMap := range devices {
		device, ok := deviceMap.(map[string]interface{})
		if !ok {
			return errors.New("Expected Devices elements to be objects")
		}

		//fmt.Printf("%d: %s\n", int(device["ID"].(float64)), device["Name"])

		// Don't want to re-add the SBP
		var deviceID = strconv.FormatFloat(device["ID"].(float64), 'f', 0, 64)
		if deviceID == smartBridgeProID {
			continue
		}
		var deviceName string = device["Name"].(string)
		gohomeDevice := makeDevice("", deviceName, deviceID, device, sbp, system, false, nil)
		//TODO: Errors
		system.AddDevice(gohomeDevice)
		sbp.AddDevice(gohomeDevice)
	}

	zones, ok := root["Zones"].([]interface{})
	if !ok {
		return errors.New("Missing Zones key")
	}

	//fmt.Println("\nZONES")
	for _, zoneMap := range zones {
		z := zoneMap.(map[string]interface{})
		//fmt.Printf("%d: %s\n", int(z["ID"].(float64)), z["Name"])

		var zoneID = strconv.FormatFloat(z["ID"].(float64), 'f', 0, 64)
		var zoneName = z["Name"].(string)
		var zoneTypeFinal = zone.ZTLight
		if zoneType, ok := z["Type"].(string); ok {
			switch zoneType {
			case "light":
				zoneTypeFinal = zone.ZTLight
			case "shade":
				zoneTypeFinal = zone.ZTShade
			}
		}
		var outputTypeFinal = zone.OTContinuous
		if outputType, ok := z["Output"].(string); ok {
			switch outputType {
			case "binary":
				outputTypeFinal = zone.OTBinary
			case "continuous":
				outputTypeFinal = zone.OTContinuous
			}
		}
		newZone := &zone.Zone{
			Address:     zoneID,
			Name:        zoneName,
			Description: zoneName,
			DeviceID:    sbp.ID,
			Type:        zoneTypeFinal,
			Output:      outputTypeFinal,
		}
		//TODO: proper logging
		err := system.AddZone(newZone)
		if err != nil {
			fmt.Printf("err add zone: %s\n", err)
		} else {
			//fmt.Printf("added zone %s with ID %s\n", newZone.Name, newZone.ID)
		}
		//sbp.Zones()[newZone.Address] = newZone
	}

	return nil
}