package mcts;

import info.kwarc.kalah.Agent;
import info.kwarc.kalah.KalahState;
import info.kwarc.kalah.ProtocolManager;

import java.io.IOException;


// agent using monte carlo tree search
class MCTSAgent extends Agent {

    public MCTSAgent(String host, Integer port, ProtocolManager.ConnectionType conType) {

        // TODO enter your data
        super(
                host,
                port,
                conType,
                "MCTSAgent",
                "LisaRebecca", // authors go here
                "", // description goes here
                "yummy", // token goes here
                true
        );
    }

    @Override
    public void search(KalahState ks) throws IOException {

        // submit some move so there is one in case we're running out of time
        submitMove(ks.randomLegalMove());

        KalahTree kt = new KalahTree();
        kt.setRootNode(new KalahNode(ks));
        KalahNode node = kt.getRootNode();
        node.expand();

        while(! shouldStop()){

            // select: vorerst RANDOM, dann UCT!!!
            KalahNode randomNode = node.getRandomChildNode();


            // expand
            randomNode.expand();
            KalahNode simulationNode = randomNode.getRandomChildNode();


            // simulate
            int playoutResult = simulationNode.simulateRandomPlayout();


            // back prop
            randomNode.backpropagate(playoutResult);

        }
    }

    public static void main(String[] args) {

        while (true) {

            Agent agent = new MCTSAgent("localhost", 2671, ProtocolManager.ConnectionType.TCP);

            try {
                agent.run();
            } catch (IOException e) {
                e.printStackTrace();
            }

            try {
                Thread.sleep(10_000);
            } catch (InterruptedException e) {
                e.printStackTrace();
            }
        }
    }

}
