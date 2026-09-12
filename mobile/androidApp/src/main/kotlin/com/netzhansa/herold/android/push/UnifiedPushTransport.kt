package com.netzhansa.herold.android.push

import android.content.Context
import org.unifiedpush.android.connector.UnifiedPush

/**
 * The distributor side of the UnifiedPush transport (REQ-AND-PUSH-04): a
 * distributor app the user already trusts carries herold's pushes, so a
 * phone without Google Play Services still gets them.
 *
 * A distributor is any installed app answering the connector's REGISTER
 * broadcast. The device may hold several (ntfy, NextPush, a ROM's own);
 * the user picks one in settings and the choice is remembered by the
 * connector. Registering hands the distributor this app's package and an
 * instance token; the endpoint comes back asynchronously as a broadcast
 * to `HeroldUnifiedPushReceiver`.
 */
object UnifiedPushTransport {

    /** The connector's instance name; this app registers one endpoint. */
    const val INSTANCE = "default"

    /** Every installed distributor, by package name. */
    fun distributors(context: Context): List<String> = UnifiedPush.getDistributors(context)

    /** The distributor the user picked, when it is still installed. */
    fun selected(context: Context): String? {
        val saved = UnifiedPush.getSavedDistributor(context).orEmpty()
        if (saved.isBlank()) return null
        // An uninstalled distributor leaves its choice behind; the endpoint
        // it handed out is dead and herold unregisters the subscription on
        // the 404 its next delivery gets.
        return saved.takeIf { it in distributors(context) }
    }

    /** True when a distributor is installed, whether or not one is picked yet. */
    fun available(context: Context): Boolean = distributors(context).isNotEmpty()

    /**
     * The distributor a registration would go to: the user's pick, or the
     * only installed one, which needs no choosing.
     */
    fun effective(context: Context): String? =
        selected(context) ?: distributors(context).singleOrNull()

    /** Records the user's pick. */
    fun select(context: Context, distributor: String) {
        UnifiedPush.saveDistributor(context, distributor)
    }

    /**
     * Asks the effective distributor for an endpoint. Repeating the call
     * is how a rotation is re-requested, so it is safe on every
     * foreground; the answer arrives as a broadcast.
     */
    fun register(context: Context): Boolean {
        val distributor = effective(context) ?: return false
        UnifiedPush.saveDistributor(context, distributor)
        UnifiedPush.registerApp(context, INSTANCE)
        return true
    }

    /**
     * Gives the endpoint back, which is what the user turning the
     * transport off or signing out asks for. The distributor answers with
     * an UNREGISTERED broadcast; herold's subscription is destroyed
     * separately, because a distributor that is already gone sends
     * nothing.
     */
    fun unregister(context: Context) {
        runCatching { UnifiedPush.unregisterApp(context, INSTANCE) }
        runCatching { UnifiedPush.safeRemoveDistributor(context) }
    }

    /** A human label for a distributor package, falling back to the package name. */
    fun label(context: Context, distributor: String): String = runCatching {
        val packages = context.packageManager
        packages.getApplicationLabel(packages.getApplicationInfo(distributor, 0)).toString()
    }.getOrDefault(distributor)
}
