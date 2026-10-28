import java.io.File

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// Versionen tas från VERSION i repots rot (eller miljövariabeln VERSION i CI).
val pvVersion: String = (System.getenv("VERSION") ?: File(rootDir, "../VERSION").readText()).trim()
val parts = pvVersion.split(".").map { it.takeWhile { c -> c.isDigit() }.toIntOrNull() ?: 0 }
val pvCode = parts.getOrElse(0) { 0 } * 10000 + parts.getOrElse(1) { 0 } * 100 + parts.getOrElse(2) { 0 }

android {
    namespace = "se.pingvakt.app"
    compileSdk = 35

    defaultConfig {
        applicationId = "se.pingvakt.app"
        minSdk = 26
        targetSdk = 35
        versionCode = pvCode
        versionName = pvVersion
    }

    signingConfigs {
        create("release") {
            // Nyckel från CI-hemligheter om de finns, annars repots egen nyckel (för sidladdning)
            val ks = System.getenv("ANDROID_KEYSTORE_FILE")
            if (ks != null && File(ks).exists()) {
                storeFile = File(ks)
                storePassword = System.getenv("ANDROID_KEYSTORE_PASSWORD")
                keyAlias = System.getenv("ANDROID_KEY_ALIAS")
                keyPassword = System.getenv("ANDROID_KEY_PASSWORD")
            } else {
                storeFile = File(rootDir, "pingvakt.keystore")
                storePassword = "pingvakt"
                keyAlias = "pingvakt"
                keyPassword = "pingvakt"
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.getByName("release")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }

    lint {
        // appen sidladdas – låt inte lint-varningar (t.ex. batterioptimering) stoppa bygget
        abortOnError = false
        checkReleaseBuilds = false
    }

    packaging {
        // agenten ligger som libpvagent.so och måste packas upp för att kunna köras
        jniLibs.useLegacyPackaging = true
    }
}

dependencies {
    implementation("androidx.core:core:1.13.1")
}
