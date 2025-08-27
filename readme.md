# APC2MQTT

Bridges between MQTT and SNMP to enable [Home Assistant][hass] to control of a network enabled APC PDU.

Uses [MQTT Discovery][mqttdisco] to automatically configure entities and devices in Home Assistant - all you should need to do is point it at the PDU and MQTT server.

Tested and working with:
- AP7921
- AP7920

[mqttdisco]: https://www.home-assistant.io/docs/mqtt/discovery/
[hass]: https://www.home-assistant.io/
