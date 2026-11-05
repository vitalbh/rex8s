/// <reference types="@raycast/api">

/* 🚧 🚧 🚧
 * This file is auto-generated from the extension's manifest.
 * Do not modify manually. Instead, update the `package.json` file.
 * 🚧 🚧 🚧 */

/* eslint-disable @typescript-eslint/ban-types */

type ExtensionPreferences = {
  /** rex8s binary - Path to rex8s. */
  "rexK8s": string,
  /** Rex app name - Brought to the front after opening a tab. */
  "rexApp": string
}

/** Preferences accessible in all the extension's commands */
declare type Preferences = ExtensionPreferences

declare namespace Preferences {
  /** Preferences accessible in the `open-cluster` command */
  export type OpenCluster = ExtensionPreferences & {}
  /** Preferences accessible in the `search-pods` command */
  export type SearchPods = ExtensionPreferences & {}
  /** Preferences accessible in the `search-services` command */
  export type SearchServices = ExtensionPreferences & {}
  /** Preferences accessible in the `flux-status` command */
  export type FluxStatus = ExtensionPreferences & {}
  /** Preferences accessible in the `failing-pods` command */
  export type FailingPods = ExtensionPreferences & {}
  /** Preferences accessible in the `search-flux` command */
  export type SearchFlux = ExtensionPreferences & {}
  /** Preferences accessible in the `kube-context` command */
  export type KubeContext = ExtensionPreferences & {}
}

declare namespace Arguments {
  /** Arguments passed to the `open-cluster` command */
  export type OpenCluster = {}
  /** Arguments passed to the `search-pods` command */
  export type SearchPods = {}
  /** Arguments passed to the `search-services` command */
  export type SearchServices = {}
  /** Arguments passed to the `flux-status` command */
  export type FluxStatus = {}
  /** Arguments passed to the `failing-pods` command */
  export type FailingPods = {}
  /** Arguments passed to the `search-flux` command */
  export type SearchFlux = {}
  /** Arguments passed to the `kube-context` command */
  export type KubeContext = {}
}

